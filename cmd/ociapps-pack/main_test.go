// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wasmdesk/ociapps"
)

// writeFiles is a helper that drops a set of (name -> bytes) under
// dir, creating subdirs as needed.
func writeFiles(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRun_HappyPath_TOML(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "src")
	writeFiles(t, in, map[string][]byte{
		"terminal.wasm": []byte("\x00asm\x01\x00\x00\x00"),
		"worker.js":     []byte("// worker"),
		"wasm_exec.js":  []byte("// exec"),
	})
	manifest := filepath.Join(dir, "manifest.toml")
	os.WriteFile(manifest, []byte(`# top
mediatype = "application/vnd.oci.image.manifest.v1+json"
[files]
"app.wasm" = "terminal.wasm"
"worker.js" = "worker.js"
"wasm_exec.js" = "wasm_exec.js"
[mediatypes]
"wasm_exec.js" = "text/javascript"
`), 0o644)
	out := filepath.Join(dir, "out")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-in", in, "-manifest", manifest, "-out", out, "-ref", "terminal:latest",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run -> %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wrote 3 layers") {
		t.Errorf("missing layer count in stdout: %q", stdout.String())
	}
	for _, f := range []string{"oci-layout", "index.json", "blobs/sha256"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
}

func TestRun_HappyPath_JSON(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "src")
	writeFiles(t, in, map[string][]byte{
		"app.wasm": []byte("\x00asm\x01\x00\x00\x00"),
	})
	manifest := filepath.Join(dir, "manifest.json")
	os.WriteFile(manifest, []byte(`{"files":{"app.wasm":"app.wasm"}}`), 0o644)
	out := filepath.Join(dir, "out")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", in, "-manifest", manifest, "-out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("run -> %d: %s", code, stderr.String())
	}
}

func TestRun_DefaultRefFromInBase(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "myapp")
	writeFiles(t, in, map[string][]byte{"app.wasm": []byte("WASM")})
	manifest := filepath.Join(dir, "m.json")
	os.WriteFile(manifest, []byte(`{"files":{"app.wasm":"app.wasm"}}`), 0o644)
	out := filepath.Join(dir, "out")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", in, "-manifest", manifest, "-out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "myapp:latest") {
		t.Errorf("expected default ref to use base(in): %q", stdout.String())
	}
}

func TestRun_BadFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-no-such-flag"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_MissingIn(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-manifest", "m"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_MissingManifestFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "/tmp"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_LoadDescriptorErr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "/tmp", "-manifest", "/no/such/file"}, &stdout, &stderr); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestRun_EmptyFiles(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "m.json")
	os.WriteFile(manifest, []byte(`{}`), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", dir, "-manifest", manifest}, &stdout, &stderr); code != 1 {
		t.Errorf("want 1 on empty files, got %d", code)
	}
}

func TestRun_StatMissing(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "m.json")
	os.WriteFile(manifest, []byte(`{"files":{"a":"absent.bin"}}`), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", dir, "-manifest", manifest}, &stdout, &stderr); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestRun_StatIsDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	manifest := filepath.Join(dir, "m.json")
	os.WriteFile(manifest, []byte(`{"files":{"a":"sub"}}`), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", dir, "-manifest", manifest}, &stdout, &stderr); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestRun_MkdirOutErr(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string][]byte{"x": []byte("y")})
	manifest := filepath.Join(dir, "m.json")
	os.WriteFile(manifest, []byte(`{"files":{"x":"x"}}`), 0o644)
	// Point -out at a path that already exists as a file -> MkdirAll fails.
	outFile := filepath.Join(dir, "outfile")
	os.WriteFile(outFile, []byte("blocker"), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", dir, "-manifest", manifest, "-out", outFile}, &stdout, &stderr); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestRun_PackErr(t *testing.T) {
	orig := packLayoutFn
	packLayoutFn = func(string, string, []ociapps.FileEntry) (string, int64, error) {
		return "", 0, errors.New("forced")
	}
	defer func() { packLayoutFn = orig }()
	dir := t.TempDir()
	writeFiles(t, dir, map[string][]byte{"x": []byte("y")})
	manifest := filepath.Join(dir, "m.json")
	os.WriteFile(manifest, []byte(`{"files":{"x":"x"}}`), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", dir, "-manifest", manifest,
		"-out", filepath.Join(dir, "out")}, &stdout, &stderr); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestInferMediaType(t *testing.T) {
	cases := []struct{ name, override, want string }{
		{"a.wasm", "", ociapps.MediaTypeLayerWasm},
		{"a.js", "", ociapps.MediaTypeLayerJS},
		{"a.json", "", ociapps.MediaTypeLayerOctet},
		{"a.wasm", "application/custom", "application/custom"},
		{"a", "", ociapps.MediaTypeLayerOctet},
	}
	for _, c := range cases {
		if got := inferMediaType(c.name, c.override); got != c.want {
			t.Errorf("inferMediaType(%q,%q) = %q, want %q", c.name, c.override, got, c.want)
		}
	}
}

// TestMain_OK exercises the main() -> run() seam by stubbing osExit
// so the test process survives.
func TestMain_OK(t *testing.T) {
	called := false
	orig := osExit
	osExit = func(code int) { called = true; _ = code }
	defer func() { osExit = orig }()
	oldArgs := os.Args
	os.Args = []string{"ociapps-pack", "-no-such-flag"}
	defer func() { os.Args = oldArgs }()
	main()
	if !called {
		t.Error("expected osExit to be called for bad flag")
	}
}

// TestMain_SuccessNoExit: a successful run leaves osExit untouched.
func TestMain_SuccessNoExit(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string][]byte{"app.wasm": []byte("WASM")})
	m := filepath.Join(dir, "m.json")
	os.WriteFile(m, []byte(`{"files":{"app.wasm":"app.wasm"}}`), 0o644)
	out := filepath.Join(dir, "out")
	exitCode := -1
	orig := osExit
	osExit = func(code int) { exitCode = code }
	defer func() { osExit = orig }()
	oldArgs := os.Args
	os.Args = []string{"ociapps-pack", "-in", dir, "-manifest", m, "-out", out}
	defer func() { os.Args = oldArgs }()
	main()
	if exitCode != -1 {
		t.Errorf("expected osExit not to be called on success, got code %d", exitCode)
	}
}
