// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteStaticTree materializes an OCI image-layout directory (as produced by
// [PackLayout]) as a static file tree matching the Distribution v2 GET API,
// so a plain static file server — GitHub Pages, an S3 bucket, `python -m
// http.server` — can serve it with no registry process and no auth:
//
//	outRoot/v2/<repo>/manifests/<tag>           (and .../manifests/<digest>)
//	outRoot/v2/<repo>/blobs/sha256:<hex>        (manifest, config, every layer)
//
// It is the on-disk twin of [ServeLayout]: the same path vocabulary, written
// once instead of served dynamically. The browser-side OCIAppsLoader requests
// exactly these paths, so a tree written here is loadable same-origin — no
// CORS, no token, no proxy. That is the point: ghcr (and every other public
// registry) refuses cross-origin browser reads, but a same-origin static
// mirror beside the page needs none of that.
//
// repo is the name to mount the layout under (e.g. "hello"); the layout
// directory itself records no repo name. The reference tag is taken from the
// index entry's org.opencontainers.image.ref.name annotation (the part after
// the last ':', so "hello:latest" -> "latest"); the manifest is always also
// written under its own digest. Returns the slash-separated relative paths
// written, sorted, for a CLI banner or a deploy manifest.
func WriteStaticTree(layoutDir, repo, outRoot string) ([]string, error) {
	if repo == "" {
		return nil, fmt.Errorf("ociapps: empty repo")
	}
	idxBody, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	if err != nil {
		return nil, err
	}
	var idx struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(idxBody, &idx); err != nil {
		return nil, fmt.Errorf("ociapps: decode index.json: %w", err)
	}
	if len(idx.Manifests) == 0 {
		return nil, fmt.Errorf("ociapps: index.json has no manifests")
	}

	manifestsOut := filepath.Join(outRoot, "v2", repo, "manifests")
	blobsOut := filepath.Join(outRoot, "v2", repo, "blobs")
	if err := os.MkdirAll(manifestsOut, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(blobsOut, 0o755); err != nil {
		return nil, err
	}

	written := make(map[string]struct{})
	rel := func(parts ...string) string {
		return filepath.ToSlash(filepath.Join(append([]string{"v2", repo}, parts...)...))
	}
	// copyBlob writes blobs/sha256/<hex> from the layout to the static
	// blobs/sha256:<hex> path the loader fetches by digest. Idempotent.
	copyBlob := func(digest string) error {
		hex := strings.TrimPrefix(digest, "sha256:")
		data, err := os.ReadFile(filepath.Join(layoutDir, "blobs", "sha256", hex))
		if err != nil {
			return err
		}
		name := "sha256:" + hex
		if err := osWriteFile(filepath.Join(blobsOut, name), data, 0o644); err != nil {
			return err
		}
		written[rel("blobs", name)] = struct{}{}
		return nil
	}

	for _, m := range idx.Manifests {
		mhex := strings.TrimPrefix(m.Digest, "sha256:")
		mBody, err := os.ReadFile(filepath.Join(layoutDir, "blobs", "sha256", mhex))
		if err != nil {
			return nil, err
		}
		// Reference targets: always the digest; plus the tag from ref.name.
		targets := []string{m.Digest}
		if rn := m.Annotations["org.opencontainers.image.ref.name"]; rn != "" {
			tag := rn
			if i := strings.LastIndex(rn, ":"); i >= 0 {
				tag = rn[i+1:]
			}
			if tag != "" {
				targets = append(targets, tag)
			}
		}
		for _, ref := range targets {
			if err := osWriteFile(filepath.Join(manifestsOut, ref), mBody, 0o644); err != nil {
				return nil, err
			}
			written[rel("manifests", ref)] = struct{}{}
		}
		// The manifest is also addressable as a blob by its digest.
		if err := copyBlob(m.Digest); err != nil {
			return nil, err
		}
		// Config + every layer blob the manifest references.
		var mf Manifest
		if err := json.Unmarshal(mBody, &mf); err != nil {
			return nil, fmt.Errorf("ociapps: decode manifest %s: %w", m.Digest, err)
		}
		if mf.Config.Digest != "" {
			if err := copyBlob(mf.Config.Digest); err != nil {
				return nil, err
			}
		}
		for _, l := range mf.Layers {
			if err := copyBlob(l.Digest); err != nil {
				return nil, err
			}
		}
	}

	out := make([]string, 0, len(written))
	for p := range written {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}
