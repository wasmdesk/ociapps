// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package ociapps loads WebAssembly applications from OCI Distribution
// v2 registries, with multi-registry cluster fallback. Each "app" is an
// OCI image whose manifest annotates each layer with the VFS-relative
// file name it represents (typically: app.wasm, worker.js,
// wasm_exec.js), so a single image can carry every artifact a browser
// needs to instantiate the wasm module.
//
// Three layers of API are exposed:
//
//   - [Registry] + [Resolver]: low-level multi-registry HTTP client. A
//     Resolver tries its registries in order and returns the first one
//     that responds successfully -- the cluster fallback the wasmdesk
//     loader needs when one mirror is offline or geo-blocked.
//
//   - [Manifest] + [Descriptor]: thin OCI image-manifest model. Other
//     OCI fields (history, config payload) are kept in the parsed JSON
//     but not interpreted; the only annotation namespace this package
//     reads is "ociapps.path/<name>" -> "sha256:..." per layer.
//
//   - [App] + [Resolver.LoadApp]: end-to-end loader that pulls a
//     manifest + every referenced blob, content-addresses them through
//     an in-memory LRU, and returns an [App] whose Files map is ready
//     to hand off to the browser side of the wasm host.
//
// On js/wasm builds an optional IndexedDB cache (file cache_wasm.go,
// build tag `js && wasm`) lets app loads survive a page reload without
// re-fetching every blob; on host builds the cache field defaults to
// a no-op so the loader is identical from the caller's POV.
//
// This package is the generalisation of go-quake1/engine/ociassets:
// the Quake loader streamed pak files keyed by "quake.path/...", this
// one streams arbitrary apps keyed by "ociapps.path/...". The fetch
// machinery, single-flight cache, and Distribution v2 wire shape are
// the same.
package ociapps
