// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Media types this package emits + accepts. Grouped in one place so the
// CLI packer and the runtime resolver agree on the wire vocabulary.
const (
	// MediaTypeManifest is the standard OCI image-manifest media type.
	// distribution:2 validates this against an internal allowlist and
	// rejects manifests with unrecognised mediaType values
	// (MANIFEST_INVALID). The app-specific identity is carried in the
	// manifest annotations + the config blob's mediaType -- both are
	// passed through verbatim by every spec-compliant registry.
	MediaTypeManifest = "application/vnd.oci.image.manifest.v1+json"

	// MediaTypeConfig is the descriptor.mediaType for the config blob.
	// Vendor suffix is allowed; the body itself is free-form JSON.
	MediaTypeConfig = "application/vnd.wasmdesk.ociapps.config.v1+json"

	// MediaTypeLayerWasm is the descriptor.mediaType for a .wasm layer.
	MediaTypeLayerWasm = "application/wasm"

	// MediaTypeLayerJS is the descriptor.mediaType for a JS layer
	// (worker.js, wasm_exec.js, etc.).
	MediaTypeLayerJS = "application/javascript"

	// MediaTypeLayerOctet is the catch-all descriptor.mediaType for
	// any layer that isn't .wasm or .js (assets, .json, etc.).
	MediaTypeLayerOctet = "application/octet-stream"

	// AnnotationPathPrefix is prepended to each VFS-relative file
	// name in the manifest's annotation map ("ociapps.path/app.wasm",
	// ...). The prefix lets the package coexist with other annotation
	// namespaces (org.opencontainers.image.*) without collision.
	AnnotationPathPrefix = "ociapps.path/"
)

// Descriptor is the standard OCI content descriptor (mediaType + digest
// + size). Optional fields (urls, annotations, platform) are kept in
// the raw JSON but not modelled here; callers that need them can
// inspect [Manifest] via [json.Unmarshal] over the raw bytes.
type Descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// Manifest is the OCI v1 image-manifest JSON shape, narrowed to the
// fields this package reads or writes. Extra fields registries add
// on the wire are tolerated (json.Unmarshal drops unknown keys).
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// ErrManifestNoLayers is returned when a manifest carries an empty
// layers array. The loader can't usefully build an [App] from a
// manifest with no blobs so we surface this as a typed error rather
// than let later fetches fail one-by-one.
var ErrManifestNoLayers = errors.New("ociapps: manifest has no layers")

// ErrManifestNoAnnotations is returned when a manifest's annotations
// map carries no entries under [AnnotationPathPrefix]. Without the
// path->digest mapping the loader has no way to translate file names
// into blob fetches, so it fails fast at LoadApp time.
var ErrManifestNoAnnotations = errors.New("ociapps: manifest has no ociapps.path/* annotations")

// DecodeManifest parses raw JSON into a [Manifest]. The schemaVersion
// is required to be 2 (the only OCI image-manifest version this
// package understands); any other value returns an error.
func DecodeManifest(body []byte) (*Manifest, error) {
	m := &Manifest{}
	if err := json.Unmarshal(body, m); err != nil {
		return nil, err
	}
	if m.SchemaVersion != 2 {
		return nil, fmt.Errorf("ociapps: manifest schemaVersion must be 2, got %d", m.SchemaVersion)
	}
	return m, nil
}

// EncodeManifest is the round-trip companion of [DecodeManifest]. It
// emits canonical JSON (two-space indent, no HTML escaping) suitable
// for writing to an OCI image-layout `blobs/sha256/<digest>` file.
func EncodeManifest(m *Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// BuildFileMap walks m.Annotations and returns a VFS-name -> digest
// map keyed without the [AnnotationPathPrefix]. The result is what
// [Resolver.LoadApp] populates [App.Files] from.
//
// Returns [ErrManifestNoAnnotations] when no annotations carry the
// ociapps.path/ prefix (likely a manifest from a different producer).
func BuildFileMap(m *Manifest) (map[string]string, error) {
	out := make(map[string]string, len(m.Annotations))
	for k, v := range m.Annotations {
		if !strings.HasPrefix(k, AnnotationPathPrefix) {
			continue
		}
		name := strings.TrimPrefix(k, AnnotationPathPrefix)
		if name == "" || v == "" {
			continue
		}
		out[name] = v
	}
	if len(out) == 0 {
		return nil, ErrManifestNoAnnotations
	}
	return out, nil
}

// VerifyDigest recomputes sha256 over data and asserts it matches the
// expected "sha256:<hex>" string. Used after a blob fetch to detect
// cache / transport corruption -- a registry that returns the wrong
// bytes for a digest must never silently feed them to a wasm loader.
func VerifyDigest(data []byte, expected string) error {
	want := strings.TrimPrefix(expected, "sha256:")
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("ociapps: digest mismatch: want %s got sha256:%s", expected, got)
	}
	return nil
}

// Sha256Digest hashes data and returns the canonical "sha256:<hex>"
// digest string. Used by the CLI packer when emitting blob filenames.
func Sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
