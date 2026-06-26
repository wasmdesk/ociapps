// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// packFixture writes two files and packs them into an OCI layout under
// dir/layout, returning the layout dir. The reference is "<repo>:latest".
func packFixture(t *testing.T, dir, repo string) string {
	t.Helper()
	srcA := filepath.Join(dir, "app.wasm")
	srcB := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(srcA, []byte("\x00asm-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcB, []byte("self.onmessage=()=>{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(dir, "layout")
	_, _, err := PackLayout(layout, repo+":latest", []FileEntry{
		{Name: "app.wasm", Path: srcA, MediaType: MediaTypeLayerWasm},
		{Name: "worker.js", Path: srcB, MediaType: MediaTypeLayerJS},
	})
	if err != nil {
		t.Fatalf("PackLayout: %v", err)
	}
	return layout
}

func TestWriteStaticTree_HappyMatchesServeLayout(t *testing.T) {
	dir := t.TempDir()
	layout := packFixture(t, dir, "hello")
	out := filepath.Join(dir, "site")

	paths, err := WriteStaticTree(layout, "hello", out)
	if err != nil {
		t.Fatalf("WriteStaticTree: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no paths written")
	}
	// Sorted + slash-separated.
	for i := 1; i < len(paths); i++ {
		if paths[i-1] >= paths[i] {
			t.Fatalf("paths not sorted: %v", paths)
		}
	}

	// The manifest served at /v2/hello/manifests/latest must byte-match what
	// ServeLayout (the dynamic twin) returns for the same path.
	wantManifest, _, status, err := ServeLayout(layout, "hello", "/v2/hello/manifests/latest")
	if err != nil || status != 200 {
		t.Fatalf("ServeLayout manifest: status=%d err=%v", status, err)
	}
	gotManifest, err := os.ReadFile(filepath.Join(out, "v2", "hello", "manifests", "latest"))
	if err != nil {
		t.Fatalf("read static manifest: %v", err)
	}
	if string(gotManifest) != string(wantManifest) {
		t.Fatal("static manifest != ServeLayout manifest")
	}

	// Every annotated file's blob must be present and byte-match ServeLayout.
	var mf Manifest
	if err := json.Unmarshal(gotManifest, &mf); err != nil {
		t.Fatal(err)
	}
	if mf.Annotations[AnnotationPathPrefix+"app.wasm"] == "" {
		t.Fatal("missing app.wasm annotation")
	}
	for _, l := range mf.Layers {
		want, _, st, err := ServeLayout(layout, "hello", "/v2/hello/blobs/"+l.Digest)
		if err != nil || st != 200 {
			t.Fatalf("ServeLayout blob %s: st=%d err=%v", l.Digest, st, err)
		}
		got, err := os.ReadFile(filepath.Join(out, "v2", "hello", "blobs", l.Digest))
		if err != nil {
			t.Fatalf("read static blob %s: %v", l.Digest, err)
		}
		if string(got) != string(want) {
			t.Fatalf("blob %s bytes differ", l.Digest)
		}
		if Sha256Digest(got) != l.Digest {
			t.Fatalf("blob %s fails its own digest", l.Digest)
		}
	}
	// Manifest is addressable as a blob by digest too.
	mDigest := Sha256Digest(gotManifest)
	if _, err := os.Stat(filepath.Join(out, "v2", "hello", "blobs", mDigest)); err != nil {
		t.Fatalf("manifest not written as blob: %v", err)
	}
	// And under its digest as a manifest reference.
	if _, err := os.Stat(filepath.Join(out, "v2", "hello", "manifests", mDigest)); err != nil {
		t.Fatalf("manifest not written under digest ref: %v", err)
	}
}

func TestWriteStaticTree_EmptyRepo(t *testing.T) {
	if _, err := WriteStaticTree(t.TempDir(), "", t.TempDir()); err == nil {
		t.Fatal("want error for empty repo")
	}
}

func TestWriteStaticTree_MissingIndex(t *testing.T) {
	if _, err := WriteStaticTree(t.TempDir(), "hello", t.TempDir()); err == nil {
		t.Fatal("want error for missing index.json")
	}
}

func TestWriteStaticTree_BadIndexJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStaticTree(dir, "hello", t.TempDir()); err == nil {
		t.Fatal("want decode error")
	}
}

func TestWriteStaticTree_NoManifests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"manifests":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStaticTree(dir, "hello", t.TempDir()); err == nil {
		t.Fatal("want error for no manifests")
	}
}

func TestWriteStaticTree_MkdirManifestsFails(t *testing.T) {
	dir := t.TempDir()
	layout := packFixture(t, dir, "hello")
	// outRoot is a regular file -> MkdirAll(outRoot/v2/hello/manifests) fails.
	outFile := filepath.Join(dir, "out-is-a-file")
	if err := os.WriteFile(outFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStaticTree(layout, "hello", outFile); err == nil {
		t.Fatal("want mkdir(manifests) error")
	}
}

func TestWriteStaticTree_MkdirBlobsFails(t *testing.T) {
	dir := t.TempDir()
	layout := packFixture(t, dir, "hello")
	out := filepath.Join(dir, "site")
	// Pre-create v2/hello/blobs as a FILE so the manifests mkdir succeeds but
	// the blobs mkdir fails.
	if err := os.MkdirAll(filepath.Join(out, "v2", "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "v2", "hello", "blobs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStaticTree(layout, "hello", out); err == nil {
		t.Fatal("want mkdir(blobs) error")
	}
}

func TestWriteStaticTree_ManifestBlobMissing(t *testing.T) {
	dir := t.TempDir()
	// index.json references a manifest digest whose blob does not exist.
	idx := `{"manifests":[{"digest":"sha256:` +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		`","annotations":{"org.opencontainers.image.ref.name":"hello:latest"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStaticTree(dir, "hello", filepath.Join(dir, "site")); err == nil {
		t.Fatal("want manifest-blob read error")
	}
}

func TestWriteStaticTree_BadManifestJSON(t *testing.T) {
	dir := t.TempDir()
	// Hand-build a layout whose manifest blob is garbage: the manifest read +
	// the manifest-as-blob copy both succeed, then json.Unmarshal fails.
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("{not a manifest")
	digest := Sha256Digest(garbage)
	hex := digest[len("sha256:"):]
	if err := os.WriteFile(filepath.Join(blobs, hex), garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	idx := `{"manifests":[{"digest":"` + digest + `","annotations":{"org.opencontainers.image.ref.name":"hello:latest"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStaticTree(dir, "hello", filepath.Join(dir, "site")); err == nil {
		t.Fatal("want manifest decode error")
	}
}

func TestWriteStaticTree_NoConfigNoTagBranch(t *testing.T) {
	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	// A valid manifest with an EMPTY config digest and no layers exercises the
	// config-skip branch. The index entry has NO ref.name annotation, so the
	// tag branch is skipped too (manifest written under its digest only).
	manifest := []byte(`{"schemaVersion":2,"config":{"digest":""},"layers":[]}`)
	digest := Sha256Digest(manifest)
	hex := digest[len("sha256:"):]
	if err := os.WriteFile(filepath.Join(blobs, hex), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	idx := `{"manifests":[{"digest":"` + digest + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "site")
	paths, err := WriteStaticTree(dir, "hello", out)
	if err != nil {
		t.Fatalf("WriteStaticTree: %v", err)
	}
	// Manifest written under digest; no "latest" tag file.
	if _, err := os.Stat(filepath.Join(out, "v2", "hello", "manifests", "latest")); !os.IsNotExist(err) {
		t.Fatal("did not expect a latest tag without ref.name")
	}
	if _, err := os.Stat(filepath.Join(out, "v2", "hello", "manifests", digest)); err != nil {
		t.Fatalf("manifest digest ref missing: %v", err)
	}
	_ = paths
}

func TestWriteStaticTree_ConfigBlobMissing(t *testing.T) {
	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	// Valid manifest referencing a config digest whose blob is absent. The
	// manifest blob itself is present (so its read + copy succeed), then the
	// config copyBlob fails its read.
	missing := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	manifest := []byte(`{"schemaVersion":2,"config":{"digest":"` + missing + `"},"layers":[]}`)
	digest := Sha256Digest(manifest)
	if err := os.WriteFile(filepath.Join(blobs, digest[len("sha256:"):]), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	idx := `{"manifests":[{"digest":"` + digest + `","annotations":{"org.opencontainers.image.ref.name":"hello:latest"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStaticTree(dir, "hello", filepath.Join(dir, "site")); err == nil {
		t.Fatal("want config-blob read error")
	}
}

func TestWriteStaticTree_LayerBlobMissing(t *testing.T) {
	dir := t.TempDir()
	layout := packFixture(t, dir, "hello")
	// Delete one layer blob so copyBlob(layer) fails its read.
	var mf Manifest
	// Find the manifest via index.json.
	idxBody, _ := os.ReadFile(filepath.Join(layout, "index.json"))
	var idx struct {
		Manifests []struct{ Digest string } `json:"manifests"`
	}
	if err := json.Unmarshal(idxBody, &idx); err != nil {
		t.Fatal(err)
	}
	mBody, _ := os.ReadFile(filepath.Join(layout, "blobs", "sha256", idx.Manifests[0].Digest[len("sha256:"):]))
	if err := json.Unmarshal(mBody, &mf); err != nil {
		t.Fatal(err)
	}
	victim := mf.Layers[0].Digest[len("sha256:"):]
	if err := os.Remove(filepath.Join(layout, "blobs", "sha256", victim)); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStaticTree(layout, "hello", filepath.Join(dir, "site")); err == nil {
		t.Fatal("want layer-blob read error")
	}
}

func TestWriteStaticTree_WriteFaults(t *testing.T) {
	orig := osWriteFile
	defer func() { osWriteFile = orig }()

	// (a) manifest write fails (first osWriteFile call, a manifests/ path).
	t.Run("manifest", func(t *testing.T) {
		dir := t.TempDir()
		layout := packFixture(t, dir, "hello")
		osWriteFile = func(name string, data []byte, perm os.FileMode) error {
			if filepath.Base(filepath.Dir(name)) == "manifests" {
				return os.ErrPermission
			}
			return orig(name, data, perm)
		}
		if _, err := WriteStaticTree(layout, "hello", filepath.Join(dir, "site")); err == nil {
			t.Fatal("want manifest write error")
		}
	})

	// (b) blob write fails (copyBlob osWriteFile path).
	t.Run("blob", func(t *testing.T) {
		dir := t.TempDir()
		layout := packFixture(t, dir, "hello")
		osWriteFile = func(name string, data []byte, perm os.FileMode) error {
			if filepath.Base(filepath.Dir(name)) == "blobs" {
				return os.ErrPermission
			}
			return orig(name, data, perm)
		}
		if _, err := WriteStaticTree(layout, "hello", filepath.Join(dir, "site")); err == nil {
			t.Fatal("want blob write error")
		}
	})
}
