<p align="center">
  <img src="https://raw.githubusercontent.com/wasmdesk/brand/main/png/color/256/wasmdesk.png" alt="wasmdesk" width="88" height="88">
</p>

<h1 align="center">ociapps</h1>
<p align="center"><strong>Stream WebAssembly apps from any OCI registry, with multi-registry cluster fallback.</strong></p>

<p align="center">
  A pure-Go (CGO=0) library + two CLI tools that pack a wasm app into
  an OCI image, push it to any OCI Distribution v2 registry, and load
  it back into a browser through a cluster of mirrors.
</p>

<p align="center">
  <a href="https://github.com/wasmdesk"><img alt="part of wasmdesk" src="https://img.shields.io/badge/wasmdesk-the%20WASM%20desktop-1a7f37?style=flat-square"></a>
  <img alt="pure Go" src="https://img.shields.io/badge/Go-CGO%3D0-00ADD8?style=flat-square&logo=go&logoColor=white">
  <img alt="WebAssembly" src="https://img.shields.io/badge/WebAssembly-js%2Fwasm-654FF0?style=flat-square&logo=webassembly&logoColor=white">
  <img alt="coverage" src="https://img.shields.io/badge/coverage-100%25-brightgreen?style=flat-square">
  <a href="LICENSE"><img alt="License: BSD-3-Clause" src="https://img.shields.io/badge/license-BSD--3--Clause-blue?style=flat-square"></a>
</p>

---

`ociapps` is the foundation the wasmdesk ecosystem uses to stream any
client wasm app from any OCI Distribution v2 registry. An "app" is an
OCI image whose manifest annotates each layer with the VFS-relative
file name it represents:

```
ociapps.path/app.wasm      = sha256:<digest>
ociapps.path/worker.js     = sha256:<digest>
ociapps.path/wasm_exec.js  = sha256:<digest>
```

The loader walks the annotations, pulls every referenced blob, and
returns an `App` struct whose `Files` map is ready to hand to the
browser side of the wasm host. The resolver tries each registry in
declaration order and returns the first one that responds, so a
single logical app reference can be served by an entire cluster of
mirrors transparently.

This package is the generalisation of `go-quake1/engine/ociassets`:
the Quake loader streamed pak files keyed by `quake.path/...`; this
one streams arbitrary apps keyed by `ociapps.path/...`. The fetch
machinery, single-flight cache, and Distribution v2 wire shape are
the same.

## Library quick start

```go
import "github.com/wasmdesk/ociapps"

r := &ociapps.Resolver{
    Registries: []ociapps.Registry{
        {URL: "https://ghcr.io"},                // primary
        {URL: "https://registry.example.com"},   // fallback
        {URL: "http://localhost:5000"},          // dev mirror
    },
}

app, err := r.LoadApp(ctx, "wasmdesk/terminal:latest")
if err != nil {
    log.Fatal(err)
}

wasmBytes := app.Files["app.wasm"]
workerSrc := app.Files["worker.js"]
// ... instantiate in the browser, e.g. through syscall/js
```

### Manual cluster control

```go
// Fetch just the manifest -- learn which mirror was healthy:
reg, manifest, err := r.FetchManifest(ctx, "wasmdesk/terminal", "latest")

// Fetch a single blob (content-addressed, cached across mirrors):
reg, body, err := r.FetchBlob(ctx, "wasmdesk/terminal", "sha256:abc...")
```

### Persistent cache (browser builds)

On `js/wasm` an [`IndexedDBCache`](cache_wasm.go) lets app loads
survive a page reload without re-fetching every blob:

```go
//go:build js && wasm

r := &ociapps.Resolver{
    Registries: []ociapps.Registry{{URL: "https://ghcr.io"}},
    Cache:      ociapps.NewIndexedDBCache("", ""), // defaults
}
```

On host builds the cache defaults to an in-memory map.

## CLI quick start

Two binaries ship under `cmd/`:

### `ociapps-pack` -- write an OCI image-layout directory

```sh
go install github.com/wasmdesk/ociapps/cmd/ociapps-pack@latest

# clients/terminal/manifest.toml
cat <<EOF > clients/terminal/manifest.toml
mediatype = "application/vnd.oci.image.manifest.v1+json"
[files]
"app.wasm"     = "terminal.wasm"
"worker.js"    = "worker.js"
"wasm_exec.js" = "wasm_exec.js"
EOF

ociapps-pack \
  -in clients/terminal \
  -manifest clients/terminal/manifest.toml \
  -out _oci \
  -ref terminal:latest
```

Both `.toml` (minimal subset) and `.json` manifests are accepted.

### `ociapps-push` -- pure-Go OCI v2 pusher (no oras, no skopeo)

```sh
go install github.com/wasmdesk/ociapps/cmd/ociapps-push@latest

ociapps-push \
  -in _oci \
  -ref localhost:5000/wasmdesk/terminal:latest
```

HTTPS endpoints work transparently when `-scheme=https` is set.

## Layout

```
.
├── doc.go             package overview
├── manifest.go        Manifest + Descriptor + digest helpers
├── resolver.go        Registry + Resolver + multi-registry fallback
├── app.go             App + Resolver.LoadApp
├── layout.go          PackLayout + ServeLayout (OCI image-layout)
├── push.go            Pusher + Distribution v2 PUT machinery
├── cache_wasm.go      js/wasm IndexedDB cache (build tag)
└── cmd/
    ├── ociapps-pack/  packer CLI
    └── ociapps-push/  pusher CLI
```

## Conventions

- **CGO=0 everywhere**, on every supported arch.
- **BSD-3-Clause** licensed; all source files carry the SPDX header.
- **Content-addressed cache**: a blob fetched from any mirror is
  cached under its digest, so the next call for the same digest --
  regardless of which mirror served it -- hits memory.
- **Annotations-first**: only layers that appear under
  `ociapps.path/<vfs-name>` are surfaced in `App.Files`. Other
  layers are ignored, which lets the same image carry auxiliary
  blobs (debug symbols, source maps) without polluting the runtime.

## Sibling

The canonical consumer of `ociapps` is [`wasmbox`](https://github.com/wasmdesk/wasmbox),
the wasmdesk window-manager + compositor. wasmbox uses `ociapps` to
load each client (terminal, dock, files, ...) from any configured
OCI registry instead of bundling them into the compositor binary.
