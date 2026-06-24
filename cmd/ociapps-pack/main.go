// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

// ociapps-pack packs a directory + a manifest descriptor into an OCI
// image-layout directory ready for `ociapps-push` (or any other OCI
// Distribution v2 pusher).
//
// Usage:
//
//	ociapps-pack -in clients/terminal -manifest manifest.toml -out _oci
//
// The manifest file describes the (VFS-name -> on-disk-relative-path)
// mapping. It can be either JSON or a minimal TOML subset:
//
//	# manifest.toml
//	mediatype = "application/vnd.oci.image.manifest.v1+json"
//	[files]
//	"worker.js"    = "worker.js"
//	"wasm_exec.js" = "wasm_exec.js"
//	"app.wasm"     = "terminal.wasm"
//
//	# manifest.json
//	{ "files": { "app.wasm": "terminal.wasm" } }
//
// In both cases the LHS is the VFS-name embedded under
// "ociapps.path/" in the OCI manifest annotations; the RHS is the
// path under the -in directory the bytes are read from. Media types
// are inferred from the VFS-name extension (.wasm/.js/* -> wasm/js/
// octet) unless the per-entry "mediatype-<name>" key is set.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wasmdesk/ociapps"
)

// osExit is the testability seam (mirrors the pattern used by the
// reference oci-pack-quake CLI). Tests swap it for a recording stub
// so they can drive every failure branch without halting the test
// binary.
var osExit = os.Exit

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		osExit(code)
	}
}

// packLayoutFn is the swappable wrapper for [ociapps.PackLayout] so
// tests can drive the unhappy path without going through a real
// filesystem.
var packLayoutFn = ociapps.PackLayout

// run is the testability seam: returns an exit code instead of
// calling os.Exit so tests can drive every branch. The two writers
// let tests intercept logging without re-routing stdout.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ociapps-pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inDir := fs.String("in", "", "Input directory holding the app files")
	manifestPath := fs.String("manifest", "", "Manifest descriptor (TOML or JSON)")
	outDir := fs.String("out", "_oci", "OCI image-layout output directory")
	ref := fs.String("ref", "", "Reference annotation, e.g. terminal:latest (defaults to base(in):latest)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *inDir == "" {
		fmt.Fprintln(stderr, "ociapps-pack: -in is required")
		return 2
	}
	if *manifestPath == "" {
		fmt.Fprintln(stderr, "ociapps-pack: -manifest is required")
		return 2
	}
	if *ref == "" {
		*ref = filepath.Base(*inDir) + ":latest"
	}

	descriptor, err := loadDescriptor(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "ociapps-pack:", err)
		return 1
	}
	if len(descriptor.Files) == 0 {
		fmt.Fprintln(stderr, "ociapps-pack: manifest has no files")
		return 1
	}

	entries := make([]ociapps.FileEntry, 0, len(descriptor.Files))
	for vfsName, relPath := range descriptor.Files {
		full := filepath.Join(*inDir, relPath)
		st, err := os.Stat(full)
		if err != nil {
			fmt.Fprintln(stderr, "ociapps-pack: stat:", err)
			return 1
		}
		if st.IsDir() {
			fmt.Fprintf(stderr, "ociapps-pack: %s is a directory\n", full)
			return 1
		}
		entries = append(entries, ociapps.FileEntry{
			Name:      vfsName,
			Path:      full,
			MediaType: inferMediaType(vfsName, descriptor.PerFile[vfsName]),
		})
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(stderr, "ociapps-pack: mkdir out:", err)
		return 1
	}
	digest, size, err := packLayoutFn(*outDir, *ref, entries)
	if err != nil {
		fmt.Fprintln(stderr, "ociapps-pack:", err)
		return 1
	}
	fmt.Fprintf(stdout, "ociapps-pack: wrote %d layers + manifest %s (%d bytes) to %s\n",
		len(entries), digest, size, *outDir)
	fmt.Fprintf(stdout, "ociapps-pack: ref annotation: %s\n", *ref)
	return 0
}

// inferMediaType picks an OCI layer mediaType from the VFS file name
// extension. The override argument (per-entry MediaType from the
// manifest) wins when non-empty.
func inferMediaType(name, override string) string {
	if override != "" {
		return override
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".wasm":
		return ociapps.MediaTypeLayerWasm
	case ".js":
		return ociapps.MediaTypeLayerJS
	}
	return ociapps.MediaTypeLayerOctet
}

// descriptor is the parsed form of the manifest file. PerFile holds
// per-entry mediaType overrides (manifest TOML key "mediatype-<vfs>").
type descriptor struct {
	MediaType string
	Files     map[string]string
	PerFile   map[string]string
}
