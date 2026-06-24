// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"errors"
	"strings"
	"testing"
)

// TestDecodeManifest_Round walks a manifest through Encode then
// Decode and expects an identity result.
func TestDecodeManifest_Round(t *testing.T) {
	m := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config: Descriptor{
			MediaType: MediaTypeConfig,
			Digest:    "sha256:" + strings.Repeat("a", 64),
			Size:      42,
		},
		Layers: []Descriptor{
			{MediaType: MediaTypeLayerWasm, Digest: "sha256:" + strings.Repeat("b", 64), Size: 1024},
		},
		Annotations: map[string]string{
			AnnotationPathPrefix + "app.wasm": "sha256:" + strings.Repeat("b", 64),
		},
	}
	body, err := EncodeManifest(m)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	got, err := DecodeManifest(body)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	if got.SchemaVersion != 2 || got.MediaType != MediaTypeManifest {
		t.Errorf("schemaVersion/mediaType mismatch: %+v", got)
	}
	if len(got.Layers) != 1 || got.Layers[0].Size != 1024 {
		t.Errorf("layers mismatch: %+v", got.Layers)
	}
	if got.Annotations[AnnotationPathPrefix+"app.wasm"] != m.Layers[0].Digest {
		t.Errorf("annotation lost")
	}
}

// TestDecodeManifest_BadJSON expects a JSON syntax error to surface.
func TestDecodeManifest_BadJSON(t *testing.T) {
	if _, err := DecodeManifest([]byte("not-json")); err == nil {
		t.Fatal("expected error on garbage input")
	}
}

// TestDecodeManifest_WrongSchema expects schemaVersion != 2 to fail.
func TestDecodeManifest_WrongSchema(t *testing.T) {
	if _, err := DecodeManifest([]byte(`{"schemaVersion":1}`)); err == nil {
		t.Fatal("expected error on schemaVersion=1")
	}
}

// TestBuildFileMap_HappyPath checks the prefix-stripping logic +
// confirms non-prefixed annotations are ignored.
func TestBuildFileMap_HappyPath(t *testing.T) {
	m := &Manifest{
		Annotations: map[string]string{
			AnnotationPathPrefix + "app.wasm":     "sha256:aa",
			AnnotationPathPrefix + "worker.js":    "sha256:bb",
			"org.opencontainers.image.created":    "1970-01-01",
			AnnotationPathPrefix + "wasm_exec.js": "sha256:cc",
		},
	}
	fm, err := BuildFileMap(m)
	if err != nil {
		t.Fatalf("BuildFileMap: %v", err)
	}
	if len(fm) != 3 {
		t.Fatalf("want 3 entries, got %d (%v)", len(fm), fm)
	}
	if fm["app.wasm"] != "sha256:aa" {
		t.Errorf("app.wasm: %v", fm["app.wasm"])
	}
}

// TestBuildFileMap_EmptyEntries verifies entries with empty name or
// empty digest are dropped (defensive against malformed input).
func TestBuildFileMap_EmptyEntries(t *testing.T) {
	m := &Manifest{
		Annotations: map[string]string{
			AnnotationPathPrefix + "":         "sha256:aa",
			AnnotationPathPrefix + "real.wasm": "",
			AnnotationPathPrefix + "ok.wasm":  "sha256:bb",
		},
	}
	fm, err := BuildFileMap(m)
	if err != nil {
		t.Fatalf("BuildFileMap: %v", err)
	}
	if len(fm) != 1 || fm["ok.wasm"] != "sha256:bb" {
		t.Fatalf("expected only ok.wasm to survive, got %v", fm)
	}
}

// TestBuildFileMap_NoAnnotations surfaces the typed error.
func TestBuildFileMap_NoAnnotations(t *testing.T) {
	m := &Manifest{Annotations: map[string]string{"foo": "bar"}}
	if _, err := BuildFileMap(m); !errors.Is(err, ErrManifestNoAnnotations) {
		t.Fatalf("want ErrManifestNoAnnotations, got %v", err)
	}
}

// TestVerifyDigest_OK + _Bad exercise both branches.
func TestVerifyDigest_OK(t *testing.T) {
	data := []byte("hello world")
	digest := Sha256Digest(data)
	if err := VerifyDigest(data, digest); err != nil {
		t.Fatalf("VerifyDigest: %v", err)
	}
}

func TestVerifyDigest_Bad(t *testing.T) {
	if err := VerifyDigest([]byte("x"), "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected mismatch error")
	}
}

// TestSha256Digest_Prefix verifies the canonical "sha256:" prefix
// is included.
func TestSha256Digest_Prefix(t *testing.T) {
	d := Sha256Digest([]byte("abc"))
	if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		t.Fatalf("unexpected digest shape: %q", d)
	}
}
