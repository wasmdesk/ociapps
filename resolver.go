// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// HTTPDoer is the minimum surface [Resolver] needs from net/http.
// Tests inject an httptest.Server-backed http.Client; production uses
// http.DefaultClient (which on wasm transparently routes through the
// browser fetch() API).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Registry is one OCI Distribution v2 endpoint. URL is the scheme +
// host (no trailing slash); future fields (Auth, TLS) will land here
// without breaking existing call sites.
type Registry struct {
	URL string
}

// Cache is the optional blob-byte cache plugged into [Resolver]. On
// host builds the default is an in-memory LRU-ish map; on js/wasm
// builds callers can swap in an IndexedDB-backed cache so app loads
// survive a page reload.
//
// Implementations must be safe for concurrent use.
type Cache interface {
	Get(digest string) ([]byte, bool)
	Put(digest string, data []byte)
}

// Resolver picks among a cluster of registries. The default policy is
// sequential fallback in declaration order: each call tries Registries
// from index 0 and returns the first one whose request succeeds (HTTP
// 200 + body decode). On all-fail the last error is surfaced verbatim
// so callers can see which registry was tried last.
type Resolver struct {
	Registries []Registry
	Client     HTTPDoer
	Cache      Cache

	// initOnce guards the lazy initialisation of Client + Cache so
	// callers can construct a Resolver as a struct literal without
	// having to remember to fill in every field.
	initOnce sync.Once
}

// ErrNoRegistries is returned by [Resolver] methods when Registries
// is empty. The fix is for the caller to populate at least one entry
// before invoking the resolver.
var ErrNoRegistries = errors.New("ociapps: resolver has no registries configured")

// init lazily sets Client + Cache to safe defaults. Called by every
// public method so a zero-value Resolver always works.
func (r *Resolver) init() {
	r.initOnce.Do(func() {
		if r.Client == nil {
			r.Client = http.DefaultClient
		}
		if r.Cache == nil {
			r.Cache = newMemoryCache()
		}
	})
}

// FetchManifest does GET /v2/<repo>/manifests/<reference> against the
// cluster. The first registry that responds 200 wins; the parsed
// manifest + the winning [Registry] are returned so callers can stick
// to the same mirror for the follow-up blob fetches.
func (r *Resolver) FetchManifest(ctx context.Context, repo, reference string) (Registry, *Manifest, error) {
	r.init()
	if len(r.Registries) == 0 {
		return Registry{}, nil, ErrNoRegistries
	}
	var lastErr error
	for _, reg := range r.Registries {
		body, err := r.getManifestBytes(ctx, reg, repo, reference)
		if err != nil {
			lastErr = err
			continue
		}
		m, err := DecodeManifest(body)
		if err != nil {
			lastErr = fmt.Errorf("ociapps: decode manifest from %s: %w", reg.URL, err)
			continue
		}
		return reg, m, nil
	}
	return Registry{}, nil, lastErr
}

// FetchBlob does GET /v2/<repo>/blobs/<digest> against the cluster.
// Bytes are content-addressed: a successful fetch from any registry
// is cached under the digest so the next call for the same blob hits
// the in-memory cache regardless of which mirror is healthy.
func (r *Resolver) FetchBlob(ctx context.Context, repo, digest string) (Registry, []byte, error) {
	r.init()
	if len(r.Registries) == 0 {
		return Registry{}, nil, ErrNoRegistries
	}
	if data, ok := r.Cache.Get(digest); ok {
		return Registry{}, data, nil
	}
	var lastErr error
	for _, reg := range r.Registries {
		body, err := r.getBlobBytes(ctx, reg, repo, digest)
		if err != nil {
			lastErr = err
			continue
		}
		if err := VerifyDigest(body, digest); err != nil {
			lastErr = fmt.Errorf("ociapps: %s: %w", reg.URL, err)
			continue
		}
		r.Cache.Put(digest, body)
		return reg, body, nil
	}
	return Registry{}, nil, lastErr
}

// getManifestBytes issues one GET /v2/<repo>/manifests/<ref> against
// a single registry. Split out from FetchManifest so the fallback
// loop reads cleanly + tests can drive each transport error branch.
func (r *Resolver) getManifestBytes(ctx context.Context, reg Registry, repo, reference string) ([]byte, error) {
	u := strings.TrimRight(reg.URL, "/") + "/v2/" + repo + "/manifests/" + reference
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("ociapps: build manifest request: %w", err)
	}
	req.Header.Set("Accept", MediaTypeManifest)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ociapps: GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ociapps: GET %s: status %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ociapps: read manifest body from %s: %w", u, err)
	}
	return body, nil
}

// getBlobBytes issues one GET /v2/<repo>/blobs/<digest> against a
// single registry. Returns the raw body; digest verification is the
// caller's responsibility (FetchBlob does it before caching).
func (r *Resolver) getBlobBytes(ctx context.Context, reg Registry, repo, digest string) ([]byte, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return nil, fmt.Errorf("ociapps: digest %q missing sha256: prefix", digest)
	}
	u := strings.TrimRight(reg.URL, "/") + "/v2/" + repo + "/blobs/" + digest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("ociapps: build blob request: %w", err)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ociapps: GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ociapps: GET %s: status %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ociapps: read blob body from %s: %w", u, err)
	}
	return body, nil
}

// memoryCache is the default [Cache] implementation: a mutex-guarded
// map keyed by digest. Bytes are content-addressed so a single map is
// safe across multiple Resolver instances if callers chose to share
// one (though the default per-Resolver cache is usually right).
type memoryCache struct {
	mu sync.RWMutex
	m  map[string][]byte
}

func newMemoryCache() *memoryCache {
	return &memoryCache{m: map[string][]byte{}}
}

func (c *memoryCache) Get(digest string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.m[digest]
	return b, ok
}

func (c *memoryCache) Put(digest string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[digest] = data
}
