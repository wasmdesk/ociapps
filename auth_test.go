// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeDoer drives a scripted sequence of responses keyed by call index.
type fakeDoer struct {
	reqs []*http.Request
	fn   func(req *http.Request, n int) (*http.Response, error)
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	n := len(f.reqs)
	f.reqs = append(f.reqs, req)
	return f.fn(req, n)
}

func mkResp(status int, body string, hdr map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: h}
}

const ghcrChallenge = `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:wasmdesk/hello:pull,push"`

func TestTokenAuth_PassThroughNon401(t *testing.T) {
	fd := &fakeDoer{fn: func(_ *http.Request, _ int) (*http.Response, error) {
		return mkResp(200, "ok", nil), nil
	}}
	d := &TokenAuthDoer{Base: fd}
	req, _ := http.NewRequest("GET", "http://r/v2/", nil)
	resp, err := d.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("got %v / %v", resp, err)
	}
	if len(fd.reqs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fd.reqs))
	}
}

func TestTokenAuth_BaseError(t *testing.T) {
	fd := &fakeDoer{fn: func(_ *http.Request, _ int) (*http.Response, error) {
		return nil, errors.New("dial")
	}}
	d := &TokenAuthDoer{Base: fd}
	req, _ := http.NewRequest("GET", "http://r/v2/", nil)
	if _, err := d.Do(req); err == nil {
		t.Fatal("want base error")
	}
}

func TestTokenAuth_ChallengeFetchRetry(t *testing.T) {
	fd := &fakeDoer{fn: func(req *http.Request, n int) (*http.Response, error) {
		switch n {
		case 0: // original -> 401 challenge
			return mkResp(401, "", map[string]string{"WWW-Authenticate": ghcrChallenge}), nil
		case 1: // token endpoint
			if req.URL.Host != "ghcr.io" || req.URL.Path != "/token" {
				t.Errorf("token URL = %s", req.URL)
			}
			if req.URL.Query().Get("service") != "ghcr.io" {
				t.Errorf("service q = %q", req.URL.Query().Get("service"))
			}
			if req.URL.Query().Get("scope") != "repository:wasmdesk/hello:pull,push" {
				t.Errorf("scope q = %q (comma-in-scope not preserved)", req.URL.Query().Get("scope"))
			}
			if req.Header.Get("Authorization") != "" {
				t.Errorf("anonymous token req should have no Authorization")
			}
			return mkResp(200, `{"token":"TKN"}`, nil), nil
		default: // retry with bearer
			if req.Header.Get("Authorization") != "Bearer TKN" {
				t.Errorf("retry Authorization = %q", req.Header.Get("Authorization"))
			}
			return mkResp(201, "", nil), nil
		}
	}}
	d := &TokenAuthDoer{Base: fd}
	req, _ := http.NewRequest("PUT", "http://ghcr.io/v2/wasmdesk/hello/manifests/latest", nil)
	resp, err := d.Do(req)
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("got %v / %v", resp, err)
	}
	if d.cached() != "TKN" {
		t.Fatalf("token not cached: %q", d.cached())
	}
	if len(fd.reqs) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(fd.reqs))
	}
}

func TestTokenAuth_BasicAuthWhenCreds(t *testing.T) {
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pat"))
	fd := &fakeDoer{fn: func(req *http.Request, n int) (*http.Response, error) {
		switch n {
		case 0:
			return mkResp(401, "", map[string]string{"WWW-Authenticate": ghcrChallenge}), nil
		case 1:
			if req.Header.Get("Authorization") != want {
				t.Errorf("token basic auth = %q want %q", req.Header.Get("Authorization"), want)
			}
			return mkResp(200, `{"token":"T"}`, nil), nil
		default:
			return mkResp(201, "", nil), nil
		}
	}}
	d := &TokenAuthDoer{Base: fd, Username: "user", Password: "pat"}
	req, _ := http.NewRequest("PUT", "http://ghcr.io/v2/x/blobs/uploads/", nil)
	if _, err := d.Do(req); err != nil {
		t.Fatal(err)
	}
}

func TestTokenAuth_Non401AfterParseFails(t *testing.T) {
	fd := &fakeDoer{fn: func(_ *http.Request, _ int) (*http.Response, error) {
		return mkResp(401, "", map[string]string{"WWW-Authenticate": `Basic realm="x"`}), nil
	}}
	d := &TokenAuthDoer{Base: fd}
	req, _ := http.NewRequest("GET", "http://r/v2/", nil)
	resp, err := d.Do(req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("want passthrough 401, got %v / %v", resp, err)
	}
	if len(fd.reqs) != 1 {
		t.Fatalf("should not fetch a token for a non-Bearer challenge")
	}
}

func TestTokenAuth_FetchTokenErrorPropagates(t *testing.T) {
	fd := &fakeDoer{fn: func(_ *http.Request, n int) (*http.Response, error) {
		if n == 0 {
			return mkResp(401, "", map[string]string{"WWW-Authenticate": ghcrChallenge}), nil
		}
		return mkResp(403, "denied", nil), nil // token endpoint refuses
	}}
	d := &TokenAuthDoer{Base: fd}
	req, _ := http.NewRequest("GET", "http://ghcr.io/v2/x/manifests/l", nil)
	if _, err := d.Do(req); err == nil {
		t.Fatal("want token-fetch error")
	}
}

func TestTokenAuth_BodyReplay(t *testing.T) {
	payload := []byte("blob-bytes")
	fd := &fakeDoer{fn: func(req *http.Request, n int) (*http.Response, error) {
		switch n {
		case 0:
			return mkResp(401, "", map[string]string{"WWW-Authenticate": ghcrChallenge}), nil
		case 1:
			return mkResp(200, `{"token":"T"}`, nil), nil
		default:
			got, _ := io.ReadAll(req.Body)
			if string(got) != string(payload) {
				t.Errorf("replayed body = %q want %q", got, payload)
			}
			return mkResp(201, "", nil), nil
		}
	}}
	d := &TokenAuthDoer{Base: fd}
	// http.NewRequest sets GetBody for a *bytes.Reader, enabling replay.
	req, _ := http.NewRequest("PUT", "http://ghcr.io/v2/x/blobs/uploads/sess?digest=sha256:00", bytes.NewReader(payload))
	if _, err := d.Do(req); err != nil {
		t.Fatal(err)
	}
}

func TestTokenAuth_GetBodyError(t *testing.T) {
	fd := &fakeDoer{fn: func(_ *http.Request, n int) (*http.Response, error) {
		switch n {
		case 0:
			return mkResp(401, "", map[string]string{"WWW-Authenticate": ghcrChallenge}), nil
		default:
			return mkResp(200, `{"token":"T"}`, nil), nil
		}
	}}
	d := &TokenAuthDoer{Base: fd}
	req, _ := http.NewRequest("PUT", "http://ghcr.io/v2/x/blobs/uploads/", strings.NewReader("x"))
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("replay boom") }
	if _, err := d.Do(req); err == nil {
		t.Fatal("want replay-body error")
	}
}

func TestTokenAuth_CachedTokenAttached(t *testing.T) {
	fd := &fakeDoer{fn: func(req *http.Request, _ int) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer CACHED" {
			t.Errorf("cached token not attached: %q", req.Header.Get("Authorization"))
		}
		return mkResp(200, "", nil), nil
	}}
	d := &TokenAuthDoer{Base: fd}
	d.store("CACHED")
	req, _ := http.NewRequest("GET", "http://ghcr.io/v2/x/manifests/l", nil)
	if _, err := d.Do(req); err != nil {
		t.Fatal(err)
	}
}

func TestFetchToken_Branches(t *testing.T) {
	// bad realm URL
	d := &TokenAuthDoer{Base: &fakeDoer{fn: func(*http.Request, int) (*http.Response, error) { return nil, nil }}}
	if _, err := d.fetchToken("http://%zz", "", ""); err == nil {
		t.Error("want bad-realm error")
	}
	// base.Do error
	d = &TokenAuthDoer{Base: &fakeDoer{fn: func(*http.Request, int) (*http.Response, error) { return nil, errors.New("net") }}}
	if _, err := d.fetchToken("http://ghcr.io/token", "ghcr.io", "s"); err == nil {
		t.Error("want net error")
	}
	// bad JSON
	d = &TokenAuthDoer{Base: &fakeDoer{fn: func(*http.Request, int) (*http.Response, error) { return mkResp(200, "{bad", nil), nil }}}
	if _, err := d.fetchToken("http://ghcr.io/token", "", ""); err == nil {
		t.Error("want decode error")
	}
	// access_token fallback
	d = &TokenAuthDoer{Base: &fakeDoer{fn: func(*http.Request, int) (*http.Response, error) { return mkResp(200, `{"access_token":"AT"}`, nil), nil }}}
	if tok, err := d.fetchToken("http://ghcr.io/token", "", ""); err != nil || tok != "AT" {
		t.Errorf("access_token fallback: %q / %v", tok, err)
	}
	// no token in response
	d = &TokenAuthDoer{Base: &fakeDoer{fn: func(*http.Request, int) (*http.Response, error) { return mkResp(200, `{}`, nil), nil }}}
	if _, err := d.fetchToken("http://ghcr.io/token", "", ""); err == nil {
		t.Error("want no-token error")
	}
}

func TestFetchToken_NewRequestError(t *testing.T) {
	orig := httpNewRequest
	httpNewRequest = func(string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("newrequest boom")
	}
	defer func() { httpNewRequest = orig }()
	d := &TokenAuthDoer{Base: &fakeDoer{fn: func(*http.Request, int) (*http.Response, error) { return nil, nil }}}
	if _, err := d.fetchToken("http://ghcr.io/token", "ghcr.io", "s"); err == nil {
		t.Fatal("want NewRequest error")
	}
}

func TestParseBearerChallenge(t *testing.T) {
	r, s, sc, ok := parseBearerChallenge(ghcrChallenge)
	if !ok || r != "https://ghcr.io/token" || s != "ghcr.io" || sc != "repository:wasmdesk/hello:pull,push" {
		t.Fatalf("parse = %q,%q,%q,%v", r, s, sc, ok)
	}
	if _, _, _, ok := parseBearerChallenge(`Basic realm="x"`); ok {
		t.Error("non-Bearer should be !ok")
	}
	if _, _, _, ok := parseBearerChallenge(`Bearer service="ghcr.io"`); ok {
		t.Error("missing realm should be !ok")
	}
	// a stray param without '=' is skipped
	if r, _, _, ok := parseBearerChallenge(`Bearer realm="x",garbage`); !ok || r != "x" {
		t.Errorf("stray param handling: %q %v", r, ok)
	}
}

func TestBasicAuthAndDefaultBase(t *testing.T) {
	if basicAuth("a", "b") != base64.StdEncoding.EncodeToString([]byte("a:b")) {
		t.Error("basicAuth encoding")
	}
	if (&TokenAuthDoer{}).base() != http.DefaultClient {
		t.Error("nil Base should default to http.DefaultClient")
	}
}
