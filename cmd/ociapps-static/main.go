// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

// ociapps-static materializes an OCI image layout (produced by ociapps-pack)
// as a static file tree matching the Distribution v2 GET API, so a plain
// static file host — GitHub Pages, an S3 bucket — can serve the app
// same-origin beside the page that loads it. No registry process, no auth,
// no CORS: ghcr and friends refuse cross-origin browser reads, but a
// same-origin static mirror needs none of that.
//
// Usage:
//
//	ociapps-static -in _oci -repo hello -out site
//
// writes, under site/:
//
//	v2/hello/manifests/<tag>        (and .../manifests/<digest>)
//	v2/hello/blobs/sha256:<hex>     (manifest, config, every layer)
//
// Point the browser-side OCIAppsLoader's registry list at the directory that
// contains v2/ (same origin as the page) and it loads the app unchanged.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/wasmdesk/ociapps"
)

var osExit = os.Exit

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		osExit(code)
	}
}

// writeStaticTreeFn is the swappable wrapper for [ociapps.WriteStaticTree] so
// tests can drive the unhappy path without crafting a broken layout on disk.
var writeStaticTreeFn = ociapps.WriteStaticTree

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ociapps-static", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "_oci", "OCI image-layout input directory")
	repo := fs.String("repo", "", "repository name to mount the layout under, e.g. hello")
	out := fs.String("out", "", "output site directory (the v2/ tree is written under it)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *repo == "" {
		fmt.Fprintln(stderr, "ociapps-static: -repo is required (e.g. -repo hello)")
		return 2
	}
	if *out == "" {
		fmt.Fprintln(stderr, "ociapps-static: -out is required (e.g. -out site)")
		return 2
	}
	paths, err := writeStaticTreeFn(*in, *repo, *out)
	if err != nil {
		fmt.Fprintln(stderr, "ociapps-static:", err)
		return 1
	}
	for _, p := range paths {
		fmt.Fprintln(stdout, p)
	}
	fmt.Fprintf(stdout, "ociapps-static: wrote %d files under %s/v2/%s\n", len(paths), *out, *repo)
	return 0
}
