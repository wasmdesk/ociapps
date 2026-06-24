// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackLayout_HappyPath(t *testing.T) {
	dir := t.TempDir()
	// Write three real input files.
	pkg := filepath.Join(dir, "src")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		"app.wasm":     []byte("\x00asm\x01\x00\x00\x00fake"),
		"worker.js":    []byte("// worker"),
		"wasm_exec.js": []byte("// exec"),
	}
	entries := make([]FileEntry, 0, len(want))
	for name, body := range want {
		p := filepath.Join(pkg, name)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		mt := MediaTypeLayerOctet
		if strings.HasSuffix(name, ".wasm") {
			mt = MediaTypeLayerWasm
		}
		if strings.HasSuffix(name, ".js") {
			mt = MediaTypeLayerJS
		}
		entries = append(entries, FileEntry{Name: name, Path: p, MediaType: mt})
	}
	out := filepath.Join(dir, "out")
	digest, size, err := PackLayout(out, "terminal:latest", entries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digest, "sha256:") || size == 0 {
		t.Errorf("bad digest/size: %q %d", digest, size)
	}
	// Confirm shape.
	for _, want := range []string{
		filepath.Join(out, "oci-layout"),
		filepath.Join(out, "index.json"),
		filepath.Join(out, "blobs", "sha256"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	// Confirm index.json references our manifest digest.
	idxBlob, _ := os.ReadFile(filepath.Join(out, "index.json"))
	if !strings.Contains(string(idxBlob), digest) {
		t.Errorf("index.json doesn't mention manifest digest")
	}
}

func TestPackLayout_EmptyReference(t *testing.T) {
	if _, _, err := PackLayout(t.TempDir(), "", []FileEntry{{Name: "x", Path: "/dev/null"}}); err == nil {
		t.Fatal("expected error on empty reference")
	}
}

func TestPackLayout_NoFiles(t *testing.T) {
	if _, _, err := PackLayout(t.TempDir(), "x:y", nil); err == nil {
		t.Fatal("expected error on empty file list")
	}
}

func TestPackLayout_MissingInput(t *testing.T) {
	if _, _, err := PackLayout(t.TempDir(), "x:y", []FileEntry{
		{Name: "absent", Path: "/no/such/file/anywhere"},
	}); err == nil {
		t.Fatal("expected error on missing input")
	}
}

// TestPackLayout_DefaultMediaType covers the `mt == ""` branch.
func TestPackLayout_DefaultMediaType(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("x"), 0o644)
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y", []FileEntry{
		{Name: "f", Path: p}, // no MediaType -> defaults to octet
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPackLayout_MkdirError points the layout at a path that already
// exists as a file, so mkdir-blobs fails.
func TestPackLayout_MkdirError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "outfile")
	if err := os.WriteFile(out, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PackLayout(out, "x:y", []FileEntry{{Name: "z", Path: out}}); err == nil {
		t.Fatal("expected mkdir failure")
	}
}

// TestPackLayout_WriteIdempotent: re-running with the same input
// short-circuits writeFileIfMissing.
func TestPackLayout_WriteIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("xyz"), 0o644)
	entries := []FileEntry{{Name: "f", Path: p, MediaType: MediaTypeLayerOctet}}
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y", entries); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y", entries); err != nil {
		t.Fatal("idempotent re-run failed")
	}
}

// TestPackLayout_ConfigMarshalErr drives the json.Marshal failure
// branch via the test seam.
func TestPackLayout_ConfigMarshalErr(t *testing.T) {
	orig := jsonMarshal
	jsonMarshal = func(any) ([]byte, error) { return nil, errors.New("boom") }
	defer func() { jsonMarshal = orig }()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("x"), 0o644)
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y",
		[]FileEntry{{Name: "f", Path: p}}); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestPackLayout_IndexMarshalErr(t *testing.T) {
	orig := jsonMarshalIndent
	jsonMarshalIndent = func(any, string, string) ([]byte, error) { return nil, errors.New("boom") }
	defer func() { jsonMarshalIndent = orig }()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("x"), 0o644)
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y",
		[]FileEntry{{Name: "f", Path: p}}); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestPackLayout_ManifestEncodeErr(t *testing.T) {
	orig := encodeManifestFn
	encodeManifestFn = func(*Manifest) ([]byte, error) { return nil, errors.New("boom") }
	defer func() { encodeManifestFn = orig }()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("x"), 0o644)
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y",
		[]FileEntry{{Name: "f", Path: p}}); err == nil {
		t.Fatal("expected encode error")
	}
}

func TestPackLayout_WriteFileErr(t *testing.T) {
	orig := osWriteFile
	osWriteFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	defer func() { osWriteFile = orig }()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	// Use the real os.WriteFile to produce the input file; only the
	// pack-time writes are stubbed.
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y",
		[]FileEntry{{Name: "f", Path: p}}); err == nil {
		t.Fatal("expected write error (blob path)")
	}
}

// TestPackLayout_WriteConfigBlobErr: blob writes succeed for layers
// (because their sha digest is content-addressed and seen first),
// but the config blob write is hit by the stub. Strategy: count the
// number of layer blobs and fail on the (layers+1)-th write.
func TestPackLayout_WriteConfigBlobErr(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "f1")
	os.WriteFile(p1, []byte("alpha"), 0o644)
	configDigest := Sha256Digest([]byte(`{"created":"1970-01-01T00:00:00Z","files":1}`))
	configHex := strings.TrimPrefix(configDigest, "sha256:")
	orig := osWriteFile
	osWriteFile = func(p string, b []byte, m os.FileMode) error {
		if filepath.Base(p) == configHex {
			return errors.New("config blob denied")
		}
		return orig(p, b, m)
	}
	defer func() { osWriteFile = orig }()
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y",
		[]FileEntry{{Name: "f", Path: p1}}); err == nil {
		t.Fatal("expected config blob write error")
	}
}

// TestPackLayout_WriteManifestBlobErr: same trick but for the
// manifest blob, identified by its known digest (computed by
// re-encoding the manifest we know PackLayout will build).
func TestPackLayout_WriteManifestBlobErr(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "f1")
	os.WriteFile(p1, []byte("alpha"), 0o644)
	// Pre-compute what manifest the production code will emit, so we
	// know the hex of the manifest blob filename to target.
	payload := []byte("alpha")
	layerDigest := Sha256Digest(payload)
	cfgBody, _ := json.Marshal(map[string]any{
		"created": "1970-01-01T00:00:00Z",
		"files":   1,
	})
	cfgDigest := Sha256Digest(cfgBody)
	m := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config:        Descriptor{MediaType: MediaTypeConfig, Digest: cfgDigest, Size: int64(len(cfgBody))},
		Layers:        []Descriptor{{MediaType: MediaTypeLayerOctet, Digest: layerDigest, Size: int64(len(payload))}},
		Annotations:   map[string]string{AnnotationPathPrefix + "f": layerDigest},
	}
	manifestBody, _ := EncodeManifest(m)
	manifestHex := strings.TrimPrefix(Sha256Digest(manifestBody), "sha256:")
	orig := osWriteFile
	osWriteFile = func(p string, b []byte, mode os.FileMode) error {
		if filepath.Base(p) == manifestHex {
			return errors.New("manifest blob denied")
		}
		return orig(p, b, mode)
	}
	defer func() { osWriteFile = orig }()
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y",
		[]FileEntry{{Name: "f", Path: p1, MediaType: MediaTypeLayerOctet}}); err == nil {
		t.Fatal("expected manifest blob write error")
	}
}

// TestPackLayout_WriteOciLayoutErr: succeeds for blobs but the
// oci-layout marker write fails. Drive by stubbing osWriteFile AFTER
// the blob writes have flushed -- we count calls.
func TestPackLayout_WriteOciLayoutErr(t *testing.T) {
	orig := osWriteFile
	calls := 0
	osWriteFile = func(p string, b []byte, m os.FileMode) error {
		calls++
		if filepath.Base(p) == "oci-layout" {
			return errors.New("denied")
		}
		return orig(p, b, m)
	}
	defer func() { osWriteFile = orig }()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y",
		[]FileEntry{{Name: "f", Path: p}}); err == nil {
		t.Fatal("expected oci-layout write error")
	}
	if calls == 0 {
		t.Error("expected at least one osWriteFile call")
	}
}

func TestPackLayout_WriteIndexErr(t *testing.T) {
	orig := osWriteFile
	osWriteFile = func(p string, b []byte, m os.FileMode) error {
		if filepath.Base(p) == "index.json" {
			return errors.New("denied")
		}
		return orig(p, b, m)
	}
	defer func() { osWriteFile = orig }()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PackLayout(filepath.Join(dir, "out"), "x:y",
		[]FileEntry{{Name: "f", Path: p}}); err == nil {
		t.Fatal("expected index.json write error")
	}
}

func TestServeLayout_HappyPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("PAYLOAD"), 0o644)
	out := filepath.Join(dir, "out")
	digest, _, err := PackLayout(out, "demo:latest", []FileEntry{{Name: "f", Path: p}})
	if err != nil {
		t.Fatal(err)
	}
	// Manifest by tag.
	body, ct, status, err := ServeLayout(out, "demo", "/v2/demo/manifests/demo:latest")
	if err != nil || status != 200 {
		t.Fatalf("got %v %d %v", err, status, err)
	}
	if ct != MediaTypeManifest {
		t.Errorf("content-type: %s", ct)
	}
	if len(body) == 0 {
		t.Errorf("empty manifest body")
	}
	// Manifest by digest.
	if _, _, st, err := ServeLayout(out, "demo", "/v2/demo/manifests/"+digest); err != nil || st != 200 {
		t.Errorf("by-digest: %d %v", st, err)
	}
	// Manifest tag-only suffix match.
	if _, _, st, err := ServeLayout(out, "demo", "/v2/demo/manifests/latest"); err != nil || st != 200 {
		t.Errorf("by-tag suffix: %d %v", st, err)
	}
	// Blob.
	payloadDigest := Sha256Digest([]byte("PAYLOAD"))
	if _, _, st, err := ServeLayout(out, "demo", "/v2/demo/blobs/"+payloadDigest); err != nil || st != 200 {
		t.Errorf("blob: %d %v", st, err)
	}
	// 404 manifest by unknown ref.
	if _, _, st, _ := ServeLayout(out, "demo", "/v2/demo/manifests/missing"); st != 404 {
		t.Errorf("missing tag: %d", st)
	}
	// 400 blob with bad digest.
	if _, _, st, _ := ServeLayout(out, "demo", "/v2/demo/blobs/md5:nope"); st != 400 {
		t.Errorf("bad digest: %d", st)
	}
	// 404 blob.
	if _, _, st, _ := ServeLayout(out, "demo", "/v2/demo/blobs/sha256:"+strings.Repeat("0", 64)); st != 404 {
		t.Errorf("missing blob: %d", st)
	}
	// Unknown path.
	if _, _, st, _ := ServeLayout(out, "demo", "/anything-else"); st != 404 {
		t.Errorf("unknown path: %d", st)
	}
}

func TestServeLayout_IndexReadError(t *testing.T) {
	if _, _, _, err := ServeLayout(t.TempDir(), "x", "/v2/x/manifests/y"); err == nil {
		t.Fatal("expected read error")
	}
}

func TestServeLayout_BadIndexJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ServeLayout(dir, "x", "/v2/x/manifests/y"); err == nil {
		t.Fatal("expected decode error")
	}
}

// TestServeLayout_ManifestBlobMissing exercises the read-error
// branch when index points at an absent blob.
func TestServeLayout_ManifestBlobMissing(t *testing.T) {
	dir := t.TempDir()
	idx := map[string]any{
		"manifests": []map[string]any{
			{
				"mediaType":   MediaTypeManifest,
				"digest":      "sha256:" + strings.Repeat("0", 64),
				"size":        0,
				"annotations": map[string]string{"org.opencontainers.image.ref.name": "x:y"},
			},
		},
	}
	body, _ := json.Marshal(idx)
	os.WriteFile(filepath.Join(dir, "index.json"), body, 0o644)
	if _, _, _, err := ServeLayout(dir, "x", "/v2/x/manifests/y"); err == nil {
		t.Fatal("expected blob read error")
	}
}

// TestServeLayout_BlobReadError: a permission-shaped error other
// than os.IsNotExist.
func TestServeLayout_BlobReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything; can't simulate permission error")
	}
	dir := t.TempDir()
	idx := map[string]any{"manifests": []map[string]any{}}
	body, _ := json.Marshal(idx)
	os.WriteFile(filepath.Join(dir, "index.json"), body, 0o644)
	blobsDir := filepath.Join(dir, "blobs", "sha256")
	os.MkdirAll(blobsDir, 0o755)
	blob := filepath.Join(blobsDir, strings.Repeat("a", 64))
	os.WriteFile(blob, []byte("x"), 0o000)
	defer os.Chmod(blob, 0o644)
	_, _, st, err := ServeLayout(dir, "x", "/v2/x/blobs/sha256:"+strings.Repeat("a", 64))
	if err == nil && st == 200 {
		t.Skip("filesystem ignored mode 0; can't drive perm-err branch")
	}
}

// TestFileEntry_ExampleFormat documents the on-disk shape via a
// simple round-trip; ensures the package's exported types remain
// JSON-serializable for callers that want to dump them.
func TestFileEntry_JSONShape(t *testing.T) {
	e := FileEntry{Name: "a", Path: "/tmp/a", MediaType: MediaTypeLayerWasm}
	// FileEntry isn't a JSON type by contract; we marshal to confirm
	// no reflect-time panic for callers that try.
	if _, err := json.Marshal(e); err != nil {
		t.Fatal(err)
	}
}

// Helper used by the WriteFileErr test to keep the production code
// path readable.
var _ = fmt.Sprintf
