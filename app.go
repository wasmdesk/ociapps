// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// App is a loaded wasm application: the parsed manifest, the union
// of its annotations, and every referenced blob keyed by the
// VFS-relative file name from the manifest's "ociapps.path/" entries.
//
// Files is ready to hand off to the wasm host -- typical consumers
// pull "app.wasm" out and instantiate it directly, mounting the
// others ("worker.js", "wasm_exec.js") on the page through a script
// tag or a Blob URL.
type App struct {
	Manifest    *Manifest
	Annotations map[string]string
	Files       map[string][]byte
}

// ErrEmptyReference is returned by [Resolver.LoadApp] when the ref
// argument is the empty string. Surface separately from a parse
// error so callers can give a precise "you forgot to set --ref"
// diagnostic.
var ErrEmptyReference = errors.New("ociapps: empty reference")

// ErrInvalidReference is returned when LoadApp's ref argument can't
// be split into a repo + tag pair.
var ErrInvalidReference = errors.New("ociapps: invalid reference")

// LoadApp pulls a manifest + all referenced blobs from the cluster.
// ref is "<repo>:<tag>" -- the registry component is taken from
// [Resolver.Registries], so the same logical reference can be served
// by every mirror without the caller having to embed a host name.
//
// Convention: each layer carries a manifest annotation
// "ociapps.path/<file>" -> "<digest>"; the same digest must appear in
// the manifest's Layers array. LoadApp does NOT trust files outside
// that annotation -- a layer with no annotation is skipped.
func (r *Resolver) LoadApp(ctx context.Context, ref string) (*App, error) {
	r.init()
	repo, tag, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	_, m, err := r.FetchManifest(ctx, repo, tag)
	if err != nil {
		return nil, err
	}
	if len(m.Layers) == 0 {
		return nil, ErrManifestNoLayers
	}
	fileMap, err := BuildFileMap(m)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(fileMap))
	for name, digest := range fileMap {
		_, body, err := r.FetchBlob(ctx, repo, digest)
		if err != nil {
			return nil, fmt.Errorf("ociapps: fetch %s (%s): %w", name, digest, err)
		}
		files[name] = body
	}
	return &App{
		Manifest:    m,
		Annotations: m.Annotations,
		Files:       files,
	}, nil
}

// parseRef splits "<repo>:<tag>" into its two parts. The registry
// part is intentionally absent -- it's carried by the [Resolver].
// Tag defaults to "latest" when the colon is omitted.
func parseRef(ref string) (repo, tag string, err error) {
	if ref == "" {
		return "", "", ErrEmptyReference
	}
	if colon := strings.LastIndexByte(ref, ':'); colon >= 0 {
		repo = ref[:colon]
		tag = ref[colon+1:]
	} else {
		repo = ref
		tag = "latest"
	}
	if repo == "" {
		return "", "", fmt.Errorf("%w: empty repo in %q", ErrInvalidReference, ref)
	}
	if tag == "" {
		return "", "", fmt.Errorf("%w: empty tag in %q", ErrInvalidReference, ref)
	}
	return repo, tag, nil
}
