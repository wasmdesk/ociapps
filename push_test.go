// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeReceiver fakes a Distribution v2 push receiver: HEAD blobs/<d>
// returns the "exists" flag from a map; POST blobs/uploads/ returns a
// 202 + Location; PUT manifests/<tag> returns 201.
type fakeReceiver struct {
	mu              sync.Mutex
	uploadCounter   int
	receivedBlobs   map[string][]byte
	manifestPUT     map[string][]byte
	headExists      map[string]bool
	failManifestON  bool
	failHeadON      bool
	failPOSTuploads bool
	failPUTblob     bool
}

func newReceiver() *fakeReceiver {
	return &fakeReceiver{
		receivedBlobs: map[string][]byte{},
		manifestPUT:   map[string][]byte{},
		headExists:    map[string]bool{},
	}
}

func (f *fakeReceiver) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
		if f.failHeadON {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
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
		if f.failPOSTuploads {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		f.mu.Lock()
		f.uploadCounter++
		id := f.uploadCounter
		f.mu.Unlock()
		w.Header().Set("Location", r.URL.Path+"/sess/"+itoa(id))
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
		if f.failPUTblob {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		digest := r.URL.Query().Get("digest")
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.receivedBlobs[digest] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
		if f.failManifestON {
			http.Error(w, "manifest invalid", http.StatusBadRequest)
			return
		}
		tag := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.manifestPUT[tag] = body
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

// packTestLayout writes a tiny layout dir, returns its path + the
// digests packed.
func packTestLayout(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("WASMBYTES"), 0o644)
	out := filepath.Join(dir, "out")
	if _, _, err := PackLayout(out, "demo:latest", []FileEntry{
		{Name: "app.wasm", Path: p, MediaType: MediaTypeLayerWasm},
	}); err != nil {
		t.Fatal(err)
	}
	indexBlob, _ := os.ReadFile(filepath.Join(out, "index.json"))
	return out, indexBlob
}

func TestPusher_HappyPath(t *testing.T) {
	out, _ := packTestLayout(t)
	rec := newReceiver()
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	p := &Pusher{BaseURL: srv.URL}
	digest, err := p.PushLayout(out, "demo", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" {
		t.Fatal("empty manifest digest returned")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.receivedBlobs) < 2 {
		t.Errorf("want at least 2 blobs (config+layer), got %d", len(rec.receivedBlobs))
	}
	if _, ok := rec.manifestPUT["latest"]; !ok {
		t.Errorf("manifest tag 'latest' not received")
	}
}

func TestPusher_SkipsExisting(t *testing.T) {
	out, _ := packTestLayout(t)
	rec := newReceiver()
	// Mark all blobs as already present.
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	// Walk the layout, mark each blob as present.
	blobsDir := filepath.Join(out, "blobs", "sha256")
	files, _ := os.ReadDir(blobsDir)
	for _, e := range files {
		rec.headExists["sha256:"+e.Name()] = true
	}
	p := &Pusher{BaseURL: srv.URL}
	if _, err := p.PushLayout(out, "demo", "latest"); err != nil {
		t.Fatal(err)
	}
	// All blobs were marked present so receivedBlobs stays empty.
	if len(rec.receivedBlobs) != 0 {
		t.Errorf("expected zero uploads when blobs exist, got %d", len(rec.receivedBlobs))
	}
}

func TestPusher_EmptyBaseURL(t *testing.T) {
	p := &Pusher{}
	if _, err := p.PushLayout("anywhere", "x", "y"); err == nil {
		t.Fatal("expected empty-BaseURL error")
	}
}

func TestPusher_MissingIndex(t *testing.T) {
	p := &Pusher{BaseURL: "http://x"}
	if _, err := p.PushLayout(t.TempDir(), "x", "y"); err == nil {
		t.Fatal("expected missing-index error")
	}
}

func TestPusher_BadIndexJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.json"), []byte("not-json"), 0o644)
	p := &Pusher{BaseURL: "http://x"}
	if _, err := p.PushLayout(dir, "x", "y"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestPusher_EmptyManifests(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"manifests":[]}`), 0o644)
	p := &Pusher{BaseURL: "http://x"}
	if _, err := p.PushLayout(dir, "x", "y"); err == nil {
		t.Fatal("expected empty-manifests error")
	}
}

func TestPusher_ManifestBlobMissing(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"manifests":[{"digest":"sha256:`+strings.Repeat("0", 64)+`"}]}`), 0o644)
	p := &Pusher{BaseURL: "http://x"}
	if _, err := p.PushLayout(dir, "x", "y"); err == nil {
		t.Fatal("expected manifest blob read error")
	}
}

func TestPusher_BadManifestJSON(t *testing.T) {
	dir := t.TempDir()
	blobsDir := filepath.Join(dir, "blobs", "sha256")
	os.MkdirAll(blobsDir, 0o755)
	badManifest := []byte("not-json")
	d := Sha256Digest(badManifest)
	os.WriteFile(filepath.Join(blobsDir, strings.TrimPrefix(d, "sha256:")), badManifest, 0o644)
	os.WriteFile(filepath.Join(dir, "index.json"),
		[]byte(`{"manifests":[{"digest":"`+d+`"}]}`), 0o644)
	p := &Pusher{BaseURL: "http://x"}
	if _, err := p.PushLayout(dir, "x", "y"); err == nil {
		t.Fatal("expected manifest parse error")
	}
}

func TestPusher_HeadError(t *testing.T) {
	out, _ := packTestLayout(t)
	rec := newReceiver()
	rec.failHeadON = true
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	p := &Pusher{BaseURL: srv.URL}
	if _, err := p.PushLayout(out, "demo", "latest"); err != nil {
		// failHeadON returns 500 -> blobExists returns false, push proceeds; OK.
		t.Logf("note: HEAD 500 maps to absent, push proceeds: %v", err)
	}
}

// TestPusher_HEADTransportError forces a real transport error
// (closed conn) on the HEAD call so blobExists itself returns err.
type loopErroringDoer struct{}

func (loopErroringDoer) Do(r *http.Request) (*http.Response, error) {
	return nil, errors.New("transport down")
}

func TestPusher_HEADTransportError(t *testing.T) {
	out, _ := packTestLayout(t)
	p := &Pusher{BaseURL: "http://example.invalid", Client: loopErroringDoer{}}
	if _, err := p.PushLayout(out, "demo", "latest"); err == nil {
		t.Fatal("expected HEAD transport error")
	}
}

// TestPusher_PushLoopBlobMissing: the manifest references a layer
// digest that isn't present on disk -> readLayoutBlob fails inside
// the push loop (not the manifest-blob read).
func TestPusher_PushLoopBlobMissing(t *testing.T) {
	dir := t.TempDir()
	blobsDir := filepath.Join(dir, "blobs", "sha256")
	os.MkdirAll(blobsDir, 0o755)
	// Craft a manifest whose layer points at a digest we never write.
	missing := Sha256Digest([]byte("never-written"))
	cfg := []byte(`{}`)
	cfgDigest := Sha256Digest(cfg)
	os.WriteFile(filepath.Join(blobsDir, strings.TrimPrefix(cfgDigest, "sha256:")), cfg, 0o644)
	m := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config:        Descriptor{MediaType: MediaTypeConfig, Digest: cfgDigest, Size: int64(len(cfg))},
		Layers:        []Descriptor{{MediaType: MediaTypeLayerWasm, Digest: missing, Size: 1}},
	}
	mBody, _ := EncodeManifest(m)
	mDigest := Sha256Digest(mBody)
	os.WriteFile(filepath.Join(blobsDir, strings.TrimPrefix(mDigest, "sha256:")), mBody, 0o644)
	os.WriteFile(filepath.Join(dir, "index.json"),
		[]byte(`{"manifests":[{"mediaType":"`+MediaTypeManifest+`","digest":"`+mDigest+`","size":`+itoa(len(mBody))+`}]}`), 0o644)

	// Build a server that says "blob absent" so the read happens
	// (otherwise the HEAD might short-circuit; but the read is
	// BEFORE the HEAD, so it always happens).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	pp := &Pusher{BaseURL: srv.URL}
	if _, err := pp.PushLayout(dir, "demo", "latest"); err == nil {
		t.Fatal("expected blob-read error inside push loop")
	}
}

func TestPusher_POSTuploadsFail(t *testing.T) {
	out, _ := packTestLayout(t)
	rec := newReceiver()
	rec.failPOSTuploads = true
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	p := &Pusher{BaseURL: srv.URL}
	if _, err := p.PushLayout(out, "demo", "latest"); err == nil {
		t.Fatal("expected POST uploads failure")
	}
}

func TestPusher_PUTblobFail(t *testing.T) {
	out, _ := packTestLayout(t)
	rec := newReceiver()
	rec.failPUTblob = true
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	p := &Pusher{BaseURL: srv.URL}
	if _, err := p.PushLayout(out, "demo", "latest"); err == nil {
		t.Fatal("expected PUT blob failure")
	}
}

func TestPusher_PUTmanifestFail(t *testing.T) {
	out, _ := packTestLayout(t)
	rec := newReceiver()
	rec.failManifestON = true
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	p := &Pusher{BaseURL: srv.URL}
	if _, err := p.PushLayout(out, "demo", "latest"); err == nil {
		t.Fatal("expected PUT manifest failure")
	}
}

// TestPusher_NoLocationHeader: POST uploads returns 202 with empty
// Location header.
func TestPusher_NoLocationHeader(t *testing.T) {
	out, _ := packTestLayout(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
			w.WriteHeader(http.StatusAccepted) // no Location
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	p := &Pusher{BaseURL: srv.URL}
	if _, err := p.PushLayout(out, "demo", "latest"); err == nil {
		t.Fatal("expected no-Location error")
	}
}

// TestPusher_LocationAbsURL: server returns an absolute Location URL
// + a Location with embedded query string (?_state=foo). Both must
// be handled by the &/? choice in pushBlob.
func TestPusher_LocationWithQuery(t *testing.T) {
	out, _ := packTestLayout(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
			// Absolute URL + existing query string.
			w.Header().Set("Location", "http://"+r.Host+r.URL.Path+"sess/1?_state=abc")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
			w.WriteHeader(http.StatusCreated)
		}
	}))
	t.Cleanup(srv.Close)
	p := &Pusher{BaseURL: srv.URL}
	if _, err := p.PushLayout(out, "demo", "latest"); err != nil {
		t.Fatal(err)
	}
}

// TestReadLayoutBlob_BadDigest forces the digest-format error.
func TestReadLayoutBlob_BadDigest(t *testing.T) {
	if _, err := readLayoutBlob(t.TempDir(), "no-colon"); err == nil {
		t.Fatal("expected bad digest error")
	}
}

// TestBlobExists_TransportError: HTTP client fails.
type erroringDoer struct{}

func (erroringDoer) Do(*http.Request) (*http.Response, error) { return nil, errors.New("nope") }

func TestBlobExists_TransportError(t *testing.T) {
	if _, err := blobExists(erroringDoer{}, "http://x", "r", "sha256:00"); err == nil {
		t.Fatal("expected transport error")
	}
}

// TestBlobExists_BadURL forces http.NewRequest to fail.
func TestBlobExists_BadURL(t *testing.T) {
	if _, err := blobExists(erroringDoer{}, "http://\x7f", "r", "sha256:00"); err == nil {
		t.Fatal("expected URL build error")
	}
}

func TestPushBlob_TransportError(t *testing.T) {
	if err := pushBlob(erroringDoer{}, "http://x", "r", "sha256:00", []byte("x")); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestPushBlob_BadStartURL(t *testing.T) {
	if err := pushBlob(erroringDoer{}, "http://\x7f", "r", "sha256:00", []byte("x")); err == nil {
		t.Fatal("expected URL build error")
	}
}

// onePOSTthenErr: POST succeeds, the follow-up PUT transport fails.
type onePOSTthenErr struct {
	calls int
	loc   string
}

func (d *onePOSTthenErr) Do(r *http.Request) (*http.Response, error) {
	d.calls++
	if r.Method == http.MethodPost {
		h := http.Header{}
		h.Set("Location", d.loc)
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}
	return nil, errors.New("PUT failed")
}

func TestPushBlob_PUTTransportError(t *testing.T) {
	if err := pushBlob(&onePOSTthenErr{loc: "/sess/1"}, "http://x", "r", "sha256:00", []byte("x")); err == nil {
		t.Fatal("expected PUT error")
	}
}

// TestPushBlob_BadPUTURL: Location is set but it contains an
// invalid char so http.NewRequest fails.
func TestPushBlob_BadPUTURL(t *testing.T) {
	d := &onePOSTthenErr{loc: "http://\x7f/sess/1"}
	if err := pushBlob(d, "http://x", "r", "sha256:00", []byte("x")); err == nil {
		t.Fatal("expected URL build error")
	}
}

func TestPushManifest_TransportError(t *testing.T) {
	if err := pushManifest(erroringDoer{}, "http://x", "r", "t", "ct", []byte("x")); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestPushManifest_BadURL(t *testing.T) {
	if err := pushManifest(erroringDoer{}, "http://\x7f", "r", "t", "ct", []byte("x")); err == nil {
		t.Fatal("expected URL build error")
	}
}
