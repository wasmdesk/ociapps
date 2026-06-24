// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

// ociapps-push pushes an OCI image layout (produced by ociapps-pack)
// to one OCI Distribution v2 registry, pure-Go + no external CLI
// dependency (no oras, no skopeo, no docker).
//
// Usage:
//
//	ociapps-push -in _oci -ref localhost:5000/wasmdesk/terminal:latest
//
// Plain HTTP only by default (matches `registry:2` defaults on
// localhost:5000). HTTPS endpoints work transparently when the -ref
// host resolves to one; a custom scheme can be forced with
// -scheme=https.
//
// Push flow per OCI Distribution v2 spec:
//
//  1. HEAD   /v2/<name>/blobs/<digest>            -> 200 if present
//  2. POST   /v2/<name>/blobs/uploads/            -> 202 + Location
//  3. PUT    <Location>?digest=sha256:<hex>       (one per absent blob)
//  4. PUT    /v2/<name>/manifests/<reference>     with the manifest body
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wasmdesk/ociapps"
)

var osExit = os.Exit

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		osExit(code)
	}
}

// pushLayoutFn is the swappable wrapper for [ociapps.Pusher.PushLayout]
// so tests can drive the unhappy path without standing up a registry.
var pushLayoutFn = func(p *ociapps.Pusher, dir, repo, tag string) (string, error) {
	return p.PushLayout(dir, repo, tag)
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ociapps-push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "_oci", "OCI image-layout input directory")
	ref := fs.String("ref", "", "Target reference, e.g. localhost:5000/wasmdesk/terminal:latest")
	scheme := fs.String("scheme", "http", "URL scheme to dial the registry on (http|https)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *ref == "" {
		fmt.Fprintln(stderr, "ociapps-push: -ref is required (e.g. localhost:5000/wasmdesk/terminal:latest)")
		return 2
	}
	registry, repo, tag, err := parseRef(*ref)
	if err != nil {
		fmt.Fprintf(stderr, "ociapps-push: bad -ref: %v\n", err)
		return 2
	}
	baseURL := *scheme + "://" + registry
	p := &ociapps.Pusher{BaseURL: baseURL}
	digest, err := pushLayoutFn(p, *in, repo, tag)
	if err != nil {
		fmt.Fprintln(stderr, "ociapps-push:", err)
		return 1
	}
	fmt.Fprintf(stdout, "ociapps-push: pushed %s/%s:%s manifest %s\n", registry, repo, tag, digest)
	return 0
}

// parseRef splits "registry/repo[:tag]" into its three parts. Tag
// defaults to "latest" when omitted. Mirrors the parseRef in the
// reference oci-push-quake CLI.
func parseRef(ref string) (registry, repo, tag string, err error) {
	slash := strings.IndexByte(ref, '/')
	if slash < 0 {
		return "", "", "", fmt.Errorf("expected registry/repo[:tag], got %q", ref)
	}
	registry = ref[:slash]
	rest := ref[slash+1:]
	if colon := strings.LastIndexByte(rest, ':'); colon >= 0 {
		repo = rest[:colon]
		tag = rest[colon+1:]
	} else {
		repo = rest
		tag = "latest"
	}
	if registry == "" || repo == "" || tag == "" {
		return "", "", "", fmt.Errorf("registry/repo/tag must all be non-empty in %q", ref)
	}
	return registry, repo, tag, nil
}
