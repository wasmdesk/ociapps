// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build js && wasm

package ociapps

import (
	"syscall/js"
)

// IndexedDBCache is a [Cache] backed by the browser's IndexedDB. App
// loads survive a page reload: a hit short-circuits the network
// fetch entirely. If the browser blocks IndexedDB (private mode,
// quota exceeded), Get returns miss + Put silently drops the write
// so the resolver's in-memory cache + network fetch still work.
//
// Object store layout:
//
//	database: c.DBName       (default "wasmdesk-ociapps")
//	store:    c.StoreName    (default "blobs")
//	key:      digest (string)
//	value:    Uint8Array
//
// Construct with [NewIndexedDBCache] and assign to [Resolver.Cache]:
//
//	r := &ociapps.Resolver{
//	    Registries: []ociapps.Registry{{URL: "https://ghcr.io"}},
//	    Cache:      ociapps.NewIndexedDBCache("", ""),
//	}
type IndexedDBCache struct {
	DBName    string
	StoreName string
}

// NewIndexedDBCache returns a cache writing to the named database +
// store. Empty strings fall back to the package defaults
// ("wasmdesk-ociapps" + "blobs") so most callers pass `"", ""`.
func NewIndexedDBCache(db, store string) *IndexedDBCache {
	if db == "" {
		db = "wasmdesk-ociapps"
	}
	if store == "" {
		store = "blobs"
	}
	return &IndexedDBCache{DBName: db, StoreName: store}
}

// openDB opens (or creates) the IndexedDB database synchronously
// from Go's POV. The bridge spins on a channel waiting for the
// IDBOpenDBRequest's onsuccess / onupgradeneeded / onerror callbacks.
// Returns a JS object representing the IDBDatabase, or js.Undefined
// on any failure (caller treats undefined as "cache unavailable").
func (c *IndexedDBCache) openDB() js.Value {
	idb := js.Global().Get("indexedDB")
	if idb.IsUndefined() || idb.IsNull() {
		return js.Undefined()
	}
	req := idb.Call("open", c.DBName, 1)
	done := make(chan js.Value, 1)
	upgrade := js.FuncOf(func(this js.Value, args []js.Value) any {
		db := req.Get("result")
		if !db.IsUndefined() && !db.Call("objectStoreNames").Call("contains", c.StoreName).Bool() {
			db.Call("createObjectStore", c.StoreName)
		}
		return nil
	})
	success := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- req.Get("result")
		return nil
	})
	failure := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- js.Undefined()
		return nil
	})
	defer upgrade.Release()
	defer success.Release()
	defer failure.Release()
	req.Set("onupgradeneeded", upgrade)
	req.Set("onsuccess", success)
	req.Set("onerror", failure)
	req.Set("onblocked", failure)
	return <-done
}

// Get reads digest's bytes back out of the object store. Misses (no
// such key, store missing, JS disabled) return (nil, false).
func (c *IndexedDBCache) Get(digest string) ([]byte, bool) {
	db := c.openDB()
	if db.IsUndefined() {
		return nil, false
	}
	tx := db.Call("transaction", c.StoreName, "readonly")
	store := tx.Call("objectStore", c.StoreName)
	req := store.Call("get", digest)
	done := make(chan js.Value, 1)
	success := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- req.Get("result")
		return nil
	})
	failure := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- js.Undefined()
		return nil
	})
	defer success.Release()
	defer failure.Release()
	req.Set("onsuccess", success)
	req.Set("onerror", failure)
	result := <-done
	if result.IsUndefined() || result.IsNull() {
		return nil, false
	}
	n := result.Get("byteLength").Int()
	out := make([]byte, n)
	js.CopyBytesToGo(out, result)
	return out, true
}

// Put writes digest's bytes into the object store. Errors are
// swallowed -- the cache is best-effort and the next page load will
// refetch through the network if the write didn't stick.
func (c *IndexedDBCache) Put(digest string, data []byte) {
	db := c.openDB()
	if db.IsUndefined() {
		return
	}
	tx := db.Call("transaction", c.StoreName, "readwrite")
	store := tx.Call("objectStore", c.StoreName)
	jsArr := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(jsArr, data)
	store.Call("put", jsArr, digest)
	done := make(chan struct{}, 1)
	complete := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- struct{}{}
		return nil
	})
	failure := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- struct{}{}
		return nil
	})
	defer complete.Release()
	defer failure.Release()
	tx.Set("oncomplete", complete)
	tx.Set("onerror", failure)
	tx.Set("onabort", failure)
	<-done
}
