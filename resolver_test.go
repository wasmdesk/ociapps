// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRegistry returns an httptest.Server that serves a single
// (repo, manifest, blobs-by-digest) triple. failOn lets a test
// force any path to 500.
type fakeRegistry struct {
	repo     string
	manifest []byte
	blobs    map[string][]byte
	failOn   map[string]bool
}

func newFakeRegistry(t *testing.T, repo string, files map[string][]byte) (*fakeRegistry, *httptest.Server) {
	t.Helper()
	// Build a manifest from the file map (each file -> 1 layer).
	layers := make([]Descriptor, 0, len(files))
	annotations := make(map[string]string, len(files))
	blobs := make(map[string][]byte, len(files))
	for name, body := range files {
		d := Sha256Digest(body)
		blobs[d] = body
		layers = append(layers, Descriptor{
			MediaType: MediaTypeLayerOctet, Digest: d, Size: int64(len(body)),
		})
		annotations[AnnotationPathPrefix+name] = d
	}
	m := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config:        Descriptor{MediaType: MediaTypeConfig, Digest: Sha256Digest([]byte("cfg")), Size: 3},
		Layers:        layers,
		Annotations:   annotations,
	}
	blobs[m.Config.Digest] = []byte("cfg")
	manifest, err := EncodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRegistry{repo: repo, manifest: manifest, blobs: blobs, failOn: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fr.failOn[r.URL.Path] {
			http.Error(w, "forced fail", http.StatusInternalServerError)
			return
		}
		mPrefix := "/v2/" + repo + "/manifests/"
		bPrefix := "/v2/" + repo + "/blobs/"
		switch {
		case strings.HasPrefix(r.URL.Path, mPrefix):
			w.Header().Set("Content-Type", MediaTypeManifest)
			w.Write(fr.manifest)
		case strings.HasPrefix(r.URL.Path, bPrefix):
			digest := strings.TrimPrefix(r.URL.Path, bPrefix)
			b, ok := fr.blobs[digest]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return fr, srv
}

func TestResolver_FetchManifest_FirstWins(t *testing.T) {
	_, srv := newFakeRegistry(t, "app/x", map[string][]byte{"app.wasm": []byte("WASM")})
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	_, m, err := r.FetchManifest(context.Background(), "app/x", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Layers) != 1 {
		t.Fatalf("want 1 layer, got %d", len(m.Layers))
	}
}

// TestResolver_FetchManifest_Fallback: 1st registry 500, 2nd serves.
func TestResolver_FetchManifest_Fallback(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	_, good := newFakeRegistry(t, "app/x", map[string][]byte{"app.wasm": []byte("WASM")})
	r := &Resolver{Registries: []Registry{{URL: bad.URL}, {URL: good.URL}}}
	reg, m, err := r.FetchManifest(context.Background(), "app/x", "latest")
	if err != nil {
		t.Fatalf("expected fallback to succeed: %v", err)
	}
	if reg.URL != good.URL {
		t.Errorf("winning registry should be good, got %s", reg.URL)
	}
	if len(m.Layers) != 1 {
		t.Errorf("layers")
	}
}

func TestResolver_FetchManifest_AllFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	r := &Resolver{Registries: []Registry{{URL: bad.URL}, {URL: bad.URL}}}
	if _, _, err := r.FetchManifest(context.Background(), "x", "y"); err == nil {
		t.Fatal("expected all-fail error")
	}
}

func TestResolver_FetchManifest_NoRegistries(t *testing.T) {
	r := &Resolver{}
	if _, _, err := r.FetchManifest(context.Background(), "x", "y"); !errors.Is(err, ErrNoRegistries) {
		t.Fatalf("want ErrNoRegistries, got %v", err)
	}
}

// TestResolver_FetchManifest_BadJSON: the first registry serves a
// non-JSON body, the second is healthy -> resolver falls through.
func TestResolver_FetchManifest_BadJSON(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-json"))
	}))
	t.Cleanup(bad.Close)
	_, good := newFakeRegistry(t, "app/x", map[string][]byte{"app.wasm": []byte("WASM")})
	r := &Resolver{Registries: []Registry{{URL: bad.URL}, {URL: good.URL}}}
	if _, _, err := r.FetchManifest(context.Background(), "app/x", "latest"); err != nil {
		t.Fatalf("expected fallback to succeed: %v", err)
	}
}

func TestResolver_FetchBlob_HappyPath(t *testing.T) {
	_, srv := newFakeRegistry(t, "app/x", map[string][]byte{"app.wasm": []byte("WASM")})
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	d := Sha256Digest([]byte("WASM"))
	_, b, err := r.FetchBlob(context.Background(), "app/x", d)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "WASM" {
		t.Errorf("body: %q", b)
	}
}

// TestResolver_FetchBlob_CacheHit: a second fetch hits the cache --
// we prove it by closing the server before the second call.
func TestResolver_FetchBlob_CacheHit(t *testing.T) {
	_, srv := newFakeRegistry(t, "app/x", map[string][]byte{"app.wasm": []byte("WASM")})
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	d := Sha256Digest([]byte("WASM"))
	if _, _, err := r.FetchBlob(context.Background(), "app/x", d); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	_, b, err := r.FetchBlob(context.Background(), "app/x", d)
	if err != nil {
		t.Fatalf("cache miss after Close: %v", err)
	}
	if string(b) != "WASM" {
		t.Errorf("body: %q", b)
	}
}

func TestResolver_FetchBlob_DigestMismatch(t *testing.T) {
	// Server returns bytes that don't match the requested digest.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("lies"))
	}))
	t.Cleanup(srv.Close)
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	d := Sha256Digest([]byte("truth"))
	if _, _, err := r.FetchBlob(context.Background(), "x", d); err == nil {
		t.Fatal("expected digest-mismatch error")
	}
}

func TestResolver_FetchBlob_NoRegistries(t *testing.T) {
	r := &Resolver{}
	if _, _, err := r.FetchBlob(context.Background(), "x", "sha256:00"); !errors.Is(err, ErrNoRegistries) {
		t.Fatal(err)
	}
}

func TestResolver_FetchBlob_BadDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
	}))
	t.Cleanup(srv.Close)
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	if _, _, err := r.FetchBlob(context.Background(), "x", "md5:nope"); err == nil {
		t.Fatal("expected error on non-sha256 digest")
	}
}

// TestResolver_FetchManifest_BadCtx forces the ctx-cancelled branch
// in http.NewRequestWithContext (impossible) + the resp transport
// error branch (close immediately).
func TestResolver_FetchManifest_TransportError(t *testing.T) {
	r := &Resolver{Registries: []Registry{{URL: "http://127.0.0.1:1"}}}
	if _, _, err := r.FetchManifest(context.Background(), "x", "y"); err == nil {
		t.Fatal("expected transport error")
	}
}

// TestResolver_NilContext drives http.NewRequestWithContext failure
// on a nil context (it panics in modern Go, so we use a closed ctx
// instead which is a portable failure surface for the transport).
func TestResolver_ClosedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, srv := newFakeRegistry(t, "app/x", map[string][]byte{"app.wasm": []byte("WASM")})
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	if _, _, err := r.FetchManifest(ctx, "app/x", "latest"); err == nil {
		t.Fatal("expected ctx-cancelled error")
	}
	d := Sha256Digest([]byte("WASM"))
	if _, _, err := r.FetchBlob(ctx, "app/x", d); err == nil {
		t.Fatal("expected ctx-cancelled error")
	}
}

// TestResolver_BadURL exercises the http.NewRequestWithContext
// failure branch: a URL containing a control character is rejected
// at request build time.
func TestResolver_BadURL(t *testing.T) {
	r := &Resolver{Registries: []Registry{{URL: "http://\x7f"}}}
	if _, _, err := r.FetchManifest(context.Background(), "x", "y"); err == nil {
		t.Fatal("expected bad-URL error")
	}
	if _, _, err := r.FetchBlob(context.Background(), "x", "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected bad-URL error")
	}
}

// TestResolver_ManifestReadError drives the io.ReadAll failure
// branch: a handler that hijacks the conn and writes a Content-Length
// header it then doesn't honor.
func TestResolver_ManifestReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijack unsupported")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n")
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	if _, _, err := r.FetchManifest(context.Background(), "x", "y"); err == nil {
		t.Fatal("expected read error")
	}
}

func TestResolver_BlobReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijack unsupported")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n")
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	if _, _, err := r.FetchBlob(context.Background(), "x", "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected read error")
	}
}

// TestMemoryCache exercises the default in-process Cache.
func TestMemoryCache(t *testing.T) {
	c := newMemoryCache()
	if _, ok := c.Get("x"); ok {
		t.Error("empty cache should miss")
	}
	c.Put("x", []byte("payload"))
	b, ok := c.Get("x")
	if !ok || string(b) != "payload" {
		t.Errorf("get/put round-trip failed: %v %v", b, ok)
	}
}

// TestResolver_CustomCache verifies callers can plug their own cache.
type recordingCache struct {
	gets int
	puts int
	m    map[string][]byte
}

func (c *recordingCache) Get(k string) ([]byte, bool) {
	c.gets++
	v, ok := c.m[k]
	return v, ok
}
func (c *recordingCache) Put(k string, v []byte) {
	c.puts++
	if c.m == nil {
		c.m = map[string][]byte{}
	}
	c.m[k] = v
}

func TestResolver_CustomCache(t *testing.T) {
	_, srv := newFakeRegistry(t, "app/x", map[string][]byte{"app.wasm": []byte("WASM")})
	cache := &recordingCache{}
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}, Cache: cache}
	d := Sha256Digest([]byte("WASM"))
	if _, _, err := r.FetchBlob(context.Background(), "app/x", d); err != nil {
		t.Fatal(err)
	}
	if cache.puts != 1 || cache.gets != 1 {
		t.Errorf("want 1 put + 1 get, got puts=%d gets=%d", cache.puts, cache.gets)
	}
}
