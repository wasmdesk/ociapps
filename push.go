// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Pusher uploads an OCI image-layout directory (produced by
// [PackLayout]) to one OCI Distribution v2 registry. Plain HTTP; no
// auth, no TLS by default -- mirrors what `registry:2` accepts on
// localhost:5000. A custom Client lets callers point at an https
// registry or attach auth headers via a wrapping HTTPDoer.
type Pusher struct {
	// Client is the HTTPDoer used for every request. nil ->
	// http.DefaultClient.
	Client HTTPDoer

	// BaseURL is the full origin (scheme + host) of the destination
	// registry, no trailing slash. Example: "http://localhost:5000".
	BaseURL string
}

// PushLayout uploads every blob + the manifest from layoutDir under
// the given repo + tag. Returns the manifest digest the registry
// confirmed (always the same as the local manifest digest -- a
// mismatch indicates the registry mutated the manifest, which is
// outside spec).
func (p *Pusher) PushLayout(layoutDir, repo, tag string) (manifestDigest string, err error) {
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	if p.BaseURL == "" {
		return "", fmt.Errorf("ociapps: pusher BaseURL is empty")
	}
	baseURL := strings.TrimRight(p.BaseURL, "/")

	indexBlob, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	if err != nil {
		return "", fmt.Errorf("ociapps: read index.json: %w", err)
	}
	var idx struct {
		Manifests []struct {
			MediaType   string            `json:"mediaType"`
			Digest      string            `json:"digest"`
			Size        int64             `json:"size"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexBlob, &idx); err != nil {
		return "", fmt.Errorf("ociapps: parse index.json: %w", err)
	}
	if len(idx.Manifests) == 0 {
		return "", fmt.Errorf("ociapps: index.json has no manifests")
	}
	entry := idx.Manifests[0]

	manifestBlob, err := readLayoutBlob(layoutDir, entry.Digest)
	if err != nil {
		return "", fmt.Errorf("ociapps: read manifest blob: %w", err)
	}
	var mani Manifest
	if err := json.Unmarshal(manifestBlob, &mani); err != nil {
		return "", fmt.Errorf("ociapps: parse manifest: %w", err)
	}

	// Push config + each layer blob in order. We HEAD first so we
	// don't re-upload blobs the registry already has (cross-tag
	// dedup happens registry-side based on the digest).
	allBlobs := append([]Descriptor{mani.Config}, mani.Layers...)
	for _, desc := range allBlobs {
		blob, err := readLayoutBlob(layoutDir, desc.Digest)
		if err != nil {
			return "", fmt.Errorf("ociapps: read blob %s: %w", desc.Digest, err)
		}
		exists, err := blobExists(client, baseURL, repo, desc.Digest)
		if err != nil {
			return "", fmt.Errorf("ociapps: HEAD blob %s: %w", desc.Digest, err)
		}
		if exists {
			continue
		}
		if err := pushBlob(client, baseURL, repo, desc.Digest, blob); err != nil {
			return "", fmt.Errorf("ociapps: push blob %s: %w", desc.Digest, err)
		}
	}

	if err := pushManifest(client, baseURL, repo, tag, entry.MediaType, manifestBlob); err != nil {
		return "", fmt.Errorf("ociapps: push manifest: %w", err)
	}
	return entry.Digest, nil
}

// readLayoutBlob reads "blobs/<algo>/<hex>" out of an OCI layout dir.
// The digest must be in "<algo>:<hex>" form.
func readLayoutBlob(layoutDir, digest string) ([]byte, error) {
	algo, hexPart, ok := strings.Cut(digest, ":")
	if !ok {
		return nil, fmt.Errorf("bad digest %q (want algo:hex)", digest)
	}
	return os.ReadFile(filepath.Join(layoutDir, "blobs", algo, hexPart))
}

// blobExists does a HEAD /v2/<repo>/blobs/<digest>. 200 -> present;
// any other status (including 404) -> absent.
func blobExists(client HTTPDoer, baseURL, repo, digest string) (bool, error) {
	u := fmt.Sprintf("%s/v2/%s/blobs/%s", baseURL, repo, url.PathEscape(digest))
	req, err := http.NewRequest(http.MethodHead, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, nil
}

// pushBlob runs the monolithic-upload path: POST -> PUT with
// ?digest=. Skips the chunked path since wasm apps are well under
// the size threshold where chunking would matter.
func pushBlob(client HTTPDoer, baseURL, repo, digest string, blob []byte) error {
	startURL := fmt.Sprintf("%s/v2/%s/blobs/uploads/", baseURL, repo)
	startReq, err := http.NewRequest(http.MethodPost, startURL, nil)
	if err != nil {
		return fmt.Errorf("build POST uploads: %w", err)
	}
	startReq.Header.Set("Content-Type", "application/octet-stream")
	startResp, err := client.Do(startReq)
	if err != nil {
		return fmt.Errorf("POST uploads: %w", err)
	}
	io.Copy(io.Discard, startResp.Body)
	startResp.Body.Close()
	if startResp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("POST uploads: status %d", startResp.StatusCode)
	}
	loc := startResp.Header.Get("Location")
	if loc == "" {
		return fmt.Errorf("POST uploads: no Location header")
	}
	if strings.HasPrefix(loc, "/") {
		loc = baseURL + loc
	}
	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}
	putURL := fmt.Sprintf("%s%sdigest=%s", loc, sep, url.QueryEscape(digest))

	req, err := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(blob))
	if err != nil {
		return fmt.Errorf("build PUT: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(blob))
	putResp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT blob: %w", err)
	}
	io.Copy(io.Discard, putResp.Body)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("PUT blob: status %d", putResp.StatusCode)
	}
	return nil
}

// pushManifest PUTs the manifest body under /v2/<repo>/manifests/<tag>.
// 201 Created is the expected response per the OCI Distribution spec.
func pushManifest(client HTTPDoer, baseURL, repo, tag, mediaType string, manifest []byte) error {
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, repo, url.PathEscape(tag))
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(manifest))
	if err != nil {
		return fmt.Errorf("build PUT manifest: %w", err)
	}
	req.Header.Set("Content-Type", mediaType)
	req.ContentLength = int64(len(manifest))
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT manifest: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("PUT manifest: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
