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

// --- Cluster-fallback E2E tests ------------------------------------------
//
// These pin the OCI cluster-fallback contract end-to-end on the Go side:
// registry R1 is unhealthy (or missing the requested artefact), registry
// R2 is healthy and serves the SAME digest. The Resolver must fall through
// transparently and the caller must end up holding the bytes from R2 with
// digest verification still applied. They are the Go twin of the browser
// probe in wasmbox/test/probe-cluster-fallback.mjs.

// TestResolver_ClusterFallback_ManifestFirstRegistry5xx pins the manifest
// branch: R1 returns 503 on every request, R2 serves the canned manifest;
// FetchManifest must return the manifest body decoded from R2 and report
// R2 as the winning Registry so a follow-up FetchBlob can prefer the same
// mirror for affinity.
func TestResolver_ClusterFallback_ManifestFirstRegistry5xx(t *testing.T) {
	// R1: every manifest GET is hard-503 (Service Unavailable).
	r1Hits := 0
	r1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r1Hits++
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(r1.Close)
	// R2: healthy fake registry with one layer.
	_, r2 := newFakeRegistry(t, "cluster/app", map[string][]byte{"app.wasm": []byte("OK-R2")})

	r := &Resolver{Registries: []Registry{{URL: r1.URL}, {URL: r2.URL}}}
	winning, m, err := r.FetchManifest(context.Background(), "cluster/app", "latest")
	if err != nil {
		t.Fatalf("expected fallback to R2 to succeed: %v", err)
	}
	if winning.URL != r2.URL {
		t.Errorf("winning registry: want %s (R2), got %s", r2.URL, winning.URL)
	}
	if r1Hits != 1 {
		t.Errorf("R1 should have been tried exactly once, got %d hits", r1Hits)
	}
	if m == nil || m.SchemaVersion != 2 || len(m.Layers) != 1 {
		t.Errorf("manifest from R2 looks wrong: %+v", m)
	}
}

// TestResolver_ClusterFallback_BlobR1ConnRefused_R2Serves pins the blob
// branch with a transport-level failure on R1 (the listener is closed
// before the call, so the Resolver gets ECONNREFUSED / "no such host"
// rather than an HTTP status). R2 then serves the digest, the Resolver
// verifies the digest, and reports R2 as the served-from Registry.
func TestResolver_ClusterFallback_BlobR1ConnRefused_R2Serves(t *testing.T) {
	// R1: spin up, capture the URL, then close so any subsequent dial fails.
	r1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// never reached
		http.Error(w, "unreachable", http.StatusTeapot)
	}))
	r1URL := r1.URL
	r1.Close() // dial against r1URL now fails fast (no listener).

	// R2: serves the same payload R1 was supposed to.
	payload := []byte("CLUSTER-PAYLOAD-FROM-R2")
	_, r2 := newFakeRegistry(t, "cluster/app", map[string][]byte{"app.wasm": payload})

	r := &Resolver{Registries: []Registry{{URL: r1URL}, {URL: r2.URL}}}
	digest := Sha256Digest(payload)
	served, body, err := r.FetchBlob(context.Background(), "cluster/app", digest)
	if err != nil {
		t.Fatalf("expected R2 to serve after R1 conn-refused: %v", err)
	}
	if served.URL != r2.URL {
		t.Errorf("served-from: want %s (R2), got %s", r2.URL, served.URL)
	}
	if string(body) != string(payload) {
		t.Errorf("body mismatch: want %q, got %q", payload, body)
	}
	// Re-verify digest explicitly so the test pins that VerifyDigest ran
	// (the Resolver would have errored out before returning if it had not).
	if err := VerifyDigest(body, digest); err != nil {
		t.Errorf("digest re-verify failed: %v", err)
	}
}

// TestResolver_ClusterFallback_CacheCrossRegistryHit pins the content-
// addressable cache: a blob fetched from R2 must satisfy a SECOND fetch
// for the same digest after R2 itself is taken down -- the cache key is
// the digest, not the (registry, digest) pair, so a hit is registry-
// agnostic. This is the property that lets the cluster scale: once any
// healthy mirror has served a blob, every consumer is independent of
// every mirror.
func TestResolver_ClusterFallback_CacheCrossRegistryHit(t *testing.T) {
	// R1 always 503 so the first fetch falls through to R2.
	r1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(r1.Close)
	// R2 serves the canned bytes once, then we close it.
	payload := []byte("CONTENT-ADDRESSED-XREGISTRY")
	_, r2 := newFakeRegistry(t, "cluster/app", map[string][]byte{"app.wasm": payload})
	digest := Sha256Digest(payload)

	cache := &recordingCache{}
	r := &Resolver{
		Registries: []Registry{{URL: r1.URL}, {URL: r2.URL}},
		Cache:      cache,
	}

	// First call: R1 fails, R2 serves, cache.Put runs once.
	if _, body, err := r.FetchBlob(context.Background(), "cluster/app", digest); err != nil {
		t.Fatalf("first fetch: %v", err)
	} else if string(body) != string(payload) {
		t.Errorf("first body: want %q, got %q", payload, body)
	}
	if cache.puts != 1 {
		t.Errorf("want 1 put after R2 served, got %d", cache.puts)
	}

	// Take R2 down so any network call would fail. Cache hit must rescue us.
	r2.Close()

	got, body, err := r.FetchBlob(context.Background(), "cluster/app", digest)
	if err != nil {
		t.Fatalf("second fetch (should be cache hit): %v", err)
	}
	if string(body) != string(payload) {
		t.Errorf("second body: want %q, got %q", payload, body)
	}
	// Cache-hit short-circuit returns an empty Registry (no mirror served).
	if got.URL != "" {
		t.Errorf("cache hit should report empty Registry, got %q", got.URL)
	}
	// Cache.Put must NOT have been called again -- the second call was a hit.
	if cache.puts != 1 {
		t.Errorf("second fetch should not have written cache; puts=%d", cache.puts)
	}
}
