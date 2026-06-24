// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoadApp_HappyPath: a complete end-to-end load against a fake
// registry. Confirms the Files map is populated + the annotations
// are surfaced.
func TestLoadApp_HappyPath(t *testing.T) {
	files := map[string][]byte{
		"app.wasm":     []byte("\x00asm\x01\x00\x00\x00"),
		"worker.js":    []byte("// worker"),
		"wasm_exec.js": []byte("// exec"),
	}
	_, srv := newFakeRegistry(t, "wasmdesk/terminal", files)
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	app, err := r.LoadApp(context.Background(), "wasmdesk/terminal:latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(app.Files) != 3 {
		t.Errorf("want 3 files, got %d", len(app.Files))
	}
	for name, body := range files {
		if got := app.Files[name]; string(got) != string(body) {
			t.Errorf("%s mismatch: %q vs %q", name, got, body)
		}
	}
	if app.Annotations[AnnotationPathPrefix+"app.wasm"] == "" {
		t.Error("annotation passthrough lost")
	}
	if app.Manifest == nil || app.Manifest.SchemaVersion != 2 {
		t.Error("manifest field lost")
	}
}

// TestLoadApp_TagDefault: when ref has no colon, tag defaults to
// "latest".
func TestLoadApp_TagDefault(t *testing.T) {
	files := map[string][]byte{"app.wasm": []byte("WASM")}
	_, srv := newFakeRegistry(t, "wasmdesk/terminal", files)
	r := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	if _, err := r.LoadApp(context.Background(), "wasmdesk/terminal"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadApp_EmptyRef(t *testing.T) {
	r := &Resolver{Registries: []Registry{{URL: "http://x"}}}
	if _, err := r.LoadApp(context.Background(), ""); !errors.Is(err, ErrEmptyReference) {
		t.Fatalf("want ErrEmptyReference, got %v", err)
	}
}

func TestLoadApp_EmptyRepo(t *testing.T) {
	r := &Resolver{Registries: []Registry{{URL: "http://x"}}}
	if _, err := r.LoadApp(context.Background(), ":latest"); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("want ErrInvalidReference, got %v", err)
	}
}

func TestLoadApp_EmptyTag(t *testing.T) {
	r := &Resolver{Registries: []Registry{{URL: "http://x"}}}
	if _, err := r.LoadApp(context.Background(), "repo:"); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("want ErrInvalidReference, got %v", err)
	}
}

// TestLoadApp_NoLayers serves a manifest with an empty layers array.
func TestLoadApp_NoLayers(t *testing.T) {
	m := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config:        Descriptor{MediaType: MediaTypeConfig, Digest: Sha256Digest([]byte("c")), Size: 1},
		Layers:        nil,
		Annotations:   map[string]string{AnnotationPathPrefix + "x": "sha256:00"},
	}
	body, _ := EncodeManifest(m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	rr := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	if _, err := rr.LoadApp(context.Background(), "repo:tag"); !errors.Is(err, ErrManifestNoLayers) {
		t.Fatalf("want ErrManifestNoLayers, got %v", err)
	}
}

// TestLoadApp_NoAnnotations: manifest with layers but no
// ociapps.path/* annotations.
func TestLoadApp_NoAnnotations(t *testing.T) {
	m := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config:        Descriptor{MediaType: MediaTypeConfig, Digest: Sha256Digest([]byte("c")), Size: 1},
		Layers: []Descriptor{
			{MediaType: MediaTypeLayerWasm, Digest: Sha256Digest([]byte("x")), Size: 1},
		},
		Annotations: map[string]string{"other.ns/x": "sha256:00"},
	}
	body, _ := EncodeManifest(m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	rr := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	if _, err := rr.LoadApp(context.Background(), "repo:tag"); !errors.Is(err, ErrManifestNoAnnotations) {
		t.Fatalf("want ErrManifestNoAnnotations, got %v", err)
	}
}

// TestLoadApp_ManifestFail: the manifest fetch fails entirely.
func TestLoadApp_ManifestFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	r := &Resolver{Registries: []Registry{{URL: bad.URL}}}
	if _, err := r.LoadApp(context.Background(), "x:y"); err == nil {
		t.Fatal("expected manifest fetch failure")
	}
}

// TestLoadApp_BlobFail: manifest succeeds, blob fails.
func TestLoadApp_BlobFail(t *testing.T) {
	// Manually craft a manifest whose layer digest points at bytes
	// the server will refuse to serve.
	annotedDigest := Sha256Digest([]byte("missing-bytes"))
	m := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config:        Descriptor{MediaType: MediaTypeConfig, Digest: Sha256Digest([]byte("c")), Size: 1},
		Layers:        []Descriptor{{MediaType: MediaTypeLayerWasm, Digest: annotedDigest, Size: 13}},
		Annotations:   map[string]string{AnnotationPathPrefix + "app.wasm": annotedDigest},
	}
	body, _ := EncodeManifest(m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	rr := &Resolver{Registries: []Registry{{URL: srv.URL}}}
	if _, err := rr.LoadApp(context.Background(), "repo:tag"); err == nil {
		t.Fatal("expected blob fetch failure")
	}
}

// TestParseRef_Permutations covers the local helper directly to
// guarantee 100% coverage on its branches without depending on the
// full LoadApp path.
func TestParseRef_Permutations(t *testing.T) {
	for _, c := range []struct {
		in       string
		repo     string
		tag      string
		wantErr  bool
		wantKind error
	}{
		{"repo:tag", "repo", "tag", false, nil},
		{"a/b:c", "a/b", "c", false, nil},
		{"only", "only", "latest", false, nil},
		{"", "", "", true, ErrEmptyReference},
		{":tag", "", "", true, ErrInvalidReference},
		{"repo:", "", "", true, ErrInvalidReference},
	} {
		repo, tag, err := parseRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
				continue
			}
			if c.wantKind != nil && !errors.Is(err, c.wantKind) {
				t.Errorf("%q: want %v, got %v", c.in, c.wantKind, err)
			}
			continue
		}
		if err != nil || repo != c.repo || tag != c.tag {
			t.Errorf("%q: got (%q,%q,%v)", c.in, repo, tag, err)
		}
	}
}

// TestApp_JSONSurface confirms App marshals cleanly to JSON if a
// consumer wants to dump it (defensive sanity, not a contract).
func TestApp_JSONSurface(t *testing.T) {
	app := &App{
		Manifest:    &Manifest{SchemaVersion: 2},
		Annotations: map[string]string{"k": "v"},
		Files:       map[string][]byte{"a": []byte("b")},
	}
	if _, err := json.Marshal(app); err != nil {
		t.Fatal(err)
	}
}
