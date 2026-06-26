// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wasmdesk/ociapps"
)

// fakeReceiver replays the minimum Distribution v2 surface ociapps
// pushes against.
type fakeReceiver struct {
	mu             sync.Mutex
	uploadID       int
	receivedBlobs  map[string][]byte
	manifests      map[string][]byte
	headExists     map[string]bool
	failManifestON bool
}

func newReceiver() *fakeReceiver {
	return &fakeReceiver{
		receivedBlobs: map[string][]byte{},
		manifests:     map[string][]byte{},
		headExists:    map[string]bool{},
	}
}

func (f *fakeReceiver) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
		digest := r.URL.Path[strings.Index(r.URL.Path, "sha256:"):]
		f.mu.Lock()
		exists := f.headExists[digest]
		f.mu.Unlock()
		if exists {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
		f.mu.Lock()
		f.uploadID++
		id := f.uploadID
		f.mu.Unlock()
		w.Header().Set("Location", r.URL.Path+"sess/"+itoa(id))
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
		digest := r.URL.Query().Get("digest")
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.receivedBlobs[digest] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
		if f.failManifestON {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		tag := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.manifests[tag] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

// packTestLayout creates a tiny layout dir on disk, ready to push.
func packTestLayout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "app.wasm"), []byte("\x00asm\x01\x00\x00\x00"), 0o644)
	out := filepath.Join(dir, "out")
	if _, _, err := ociapps.PackLayout(out, "demo:latest",
		[]ociapps.FileEntry{{Name: "app.wasm", Path: filepath.Join(src, "app.wasm"),
			MediaType: ociapps.MediaTypeLayerWasm}}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRun_HappyPath(t *testing.T) {
	out := packTestLayout(t)
	rec := newReceiver()
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-in", out,
		"-ref", host + "/demo:latest",
		"-scheme", "http",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run -> %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pushed "+host+"/demo:latest") {
		t.Errorf("missing success line: %q", stdout.String())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if _, ok := rec.manifests["latest"]; !ok {
		t.Error("manifest tag 'latest' missing on server")
	}
}

func TestRun_BadFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-no-such"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_MissingRef(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "x"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_BadRef(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "x", "-ref", "noslash"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_PushErr(t *testing.T) {
	orig := pushLayoutFn
	pushLayoutFn = func(*ociapps.Pusher, string, string, string) (string, error) {
		return "", errors.New("forced")
	}
	defer func() { pushLayoutFn = orig }()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "x", "-ref", "h/r:t"}, &stdout, &stderr); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestRun_AuthWiring(t *testing.T) {
	origGet, origPush := osGetenv, pushLayoutFn
	defer func() { osGetenv, pushLayoutFn = origGet, origPush }()

	var captured *ociapps.Pusher
	pushLayoutFn = func(p *ociapps.Pusher, _, _, _ string) (string, error) {
		captured = p
		return "sha256:x", nil
	}

	cases := []struct {
		name     string
		env      map[string]string
		wantAuth bool
	}{
		{"ghcr-token", map[string]string{"GHCR_TOKEN": "tok"}, true},
		{"github-token-fallback", map[string]string{"GITHUB_TOKEN": "tok"}, true},
		{"no-creds", map[string]string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			osGetenv = func(k string) string { return c.env[k] }
			captured = nil
			var out, errb bytes.Buffer
			code := run([]string{"-in", "x", "-ref", "ghcr.io/wasmdesk/hello:latest", "-scheme", "https", "-username", "u"}, &out, &errb)
			if code != 0 {
				t.Fatalf("run=%d: %s", code, errb.String())
			}
			d, isAuth := captured.Client.(*ociapps.TokenAuthDoer)
			if isAuth != c.wantAuth {
				t.Fatalf("auth wired=%v want=%v", isAuth, c.wantAuth)
			}
			if c.wantAuth && (d.Username != "u" || d.Password != "tok") {
				t.Errorf("creds = %q/%q", d.Username, d.Password)
			}
		})
	}
}

func TestParseRef_All(t *testing.T) {
	cases := []struct {
		in                  string
		reg, repo, tag      string
		wantErr             bool
	}{
		{"ghcr.io/wasmdesk/term:1.0", "ghcr.io", "wasmdesk/term", "1.0", false},
		{"localhost:5000/x:y", "localhost:5000", "x", "y", false},
		{"noslash", "", "", "", true},
		{"/repo:t", "", "", "", true},
		{"reg/:t", "", "", "", true},
		{"reg/r:", "", "", "", true},
		{"reg/r", "reg", "r", "latest", false},
	}
	for _, c := range cases {
		reg, repo, tag, err := parseRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if reg != c.reg || repo != c.repo || tag != c.tag {
			t.Errorf("%q: got (%q,%q,%q)", c.in, reg, repo, tag)
		}
	}
}

func TestMain_BadFlag(t *testing.T) {
	called := false
	orig := osExit
	osExit = func(int) { called = true }
	defer func() { osExit = orig }()
	oldArgs := os.Args
	os.Args = []string{"ociapps-push", "-no-such"}
	defer func() { os.Args = oldArgs }()
	main()
	if !called {
		t.Error("expected osExit to fire on bad flag")
	}
}

func TestMain_OK(t *testing.T) {
	out := packTestLayout(t)
	rec := newReceiver()
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	exitCalled := false
	orig := osExit
	osExit = func(int) { exitCalled = true }
	defer func() { osExit = orig }()
	oldArgs := os.Args
	os.Args = []string{"ociapps-push", "-in", out, "-ref", host + "/demo:latest", "-scheme", "http"}
	defer func() { os.Args = oldArgs }()
	main()
	if exitCalled {
		t.Error("expected osExit NOT to fire on success")
	}
}
