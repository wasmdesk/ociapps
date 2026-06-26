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

// packTestLayout creates a tiny OCI layout dir on disk, ready to mirror.
func packTestLayout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "app.wasm")
	if err := os.WriteFile(src, []byte("\x00asm\x01\x00\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "layout")
	if _, _, err := ociapps.PackLayout(out, "hello:latest",
		[]ociapps.FileEntry{{Name: "app.wasm", Path: src, MediaType: ociapps.MediaTypeLayerWasm}}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRun_HappyPath(t *testing.T) {
	layout := packTestLayout(t)
	site := filepath.Join(t.TempDir(), "site")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-in", layout, "-repo", "hello", "-out", site}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run -> %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wrote ") {
		t.Errorf("missing summary line: %q", stdout.String())
	}
	// The manifest the loader fetches must exist.
	if _, err := os.Stat(filepath.Join(site, "v2", "hello", "manifests", "latest")); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}

func TestRun_BadFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-no-such"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_MissingRepo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "x", "-out", "y"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_MissingOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "x", "-repo", "hello"}, &stdout, &stderr); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

func TestRun_WriteErr(t *testing.T) {
	orig := writeStaticTreeFn
	writeStaticTreeFn = func(string, string, string) ([]string, error) {
		return nil, errors.New("forced")
	}
	defer func() { writeStaticTreeFn = orig }()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "x", "-repo", "hello", "-out", "y"}, &stdout, &stderr); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestMain_BadFlag(t *testing.T) {
	called := false
	orig := osExit
	osExit = func(int) { called = true }
	defer func() { osExit = orig }()
	oldArgs := os.Args
	os.Args = []string{"ociapps-static", "-no-such"}
	defer func() { os.Args = oldArgs }()
	main()
	if !called {
		t.Error("expected osExit to fire on bad flag")
	}
}

func TestMain_OK(t *testing.T) {
	layout := packTestLayout(t)
	site := filepath.Join(t.TempDir(), "site")
	exitCalled := false
	orig := osExit
	osExit = func(int) { exitCalled = true }
	defer func() { osExit = orig }()
	oldArgs := os.Args
	os.Args = []string{"ociapps-static", "-in", layout, "-repo", "hello", "-out", site}
	defer func() { os.Args = oldArgs }()
	main()
	if exitCalled {
		t.Error("expected osExit NOT to fire on success")
	}
}
