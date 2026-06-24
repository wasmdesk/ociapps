// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTOMLDescriptor_Happy(t *testing.T) {
	body := []byte(`
mediatype = "application/vnd.oci.image.manifest.v1+json"

# A comment
[files]
"app.wasm" = "client.wasm"
worker = "worker.js"   # inline comment

[mediatypes]
"app.wasm" = "application/wasm"
`)
	d, err := parseTOMLDescriptor(body)
	if err != nil {
		t.Fatal(err)
	}
	if d.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("mediatype: %q", d.MediaType)
	}
	if d.Files["app.wasm"] != "client.wasm" || d.Files["worker"] != "worker.js" {
		t.Errorf("files: %v", d.Files)
	}
	if d.PerFile["app.wasm"] != "application/wasm" {
		t.Errorf("per-file: %v", d.PerFile)
	}
}

func TestParseTOMLDescriptor_UnknownSection(t *testing.T) {
	_, err := parseTOMLDescriptor([]byte(`[nope]
a = "b"
`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseTOMLDescriptor_UnknownTopKey(t *testing.T) {
	_, err := parseTOMLDescriptor([]byte(`weird = "x"`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseTOMLDescriptor_MissingEq(t *testing.T) {
	_, err := parseTOMLDescriptor([]byte(`[files]
no-equal-here
`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseTOMLDescriptor_UnquotedValue(t *testing.T) {
	_, err := parseTOMLDescriptor([]byte(`[files]
"a" = bare
`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseJSONDescriptor_BadJSON(t *testing.T) {
	if _, err := parseJSONDescriptor([]byte("not-json")); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadDescriptor_JSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.JSON") // uppercase ext to exercise ToLower
	os.WriteFile(p, []byte(`{"files":{"a":"b"}}`), 0o644)
	d, err := loadDescriptor(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Files["a"] != "b" {
		t.Errorf("files: %v", d.Files)
	}
}

func TestLoadDescriptor_TOMLDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.txt")
	os.WriteFile(p, []byte(`[files]
"x" = "y"
`), 0o644)
	d, err := loadDescriptor(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Files["x"] != "y" {
		t.Errorf("files: %v", d.Files)
	}
}

func TestLoadDescriptor_MissingFile(t *testing.T) {
	if _, err := loadDescriptor("/no/such/path/anywhere"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStripComment(t *testing.T) {
	cases := []struct{ in, want string }{
		{`a = "b" # comment`, `a = "b"`},
		{`a = "b#notcomment"`, `a = "b#notcomment"`},
		{`# leading`, ``},
		{`no comment here`, `no comment here`},
	}
	for _, c := range cases {
		if got := stripComment(c.in); got != c.want {
			t.Errorf("stripComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseKV_QuotedKey(t *testing.T) {
	k, v, err := parseKV(`"my key" = "my val"`)
	if err != nil || k != "my key" || v != "my val" {
		t.Errorf("got (%q,%q,%v)", k, v, err)
	}
}

func TestParseTOMLDescriptor_SectionWhitespace(t *testing.T) {
	d, err := parseTOMLDescriptor([]byte(`[ files ]
a = "b"
`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Files["a"] != "b" {
		t.Errorf("files: %v", d.Files)
	}
	// Confirm section detection trimmed the spaces.
	_ = strings.TrimSpace
}
