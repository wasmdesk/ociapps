// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// loadDescriptor reads the manifest file at path and dispatches on
// extension: ".json" -> JSON, everything else -> minimal TOML. The
// TOML subset accepted is what the CLI's documentation promises:
// top-level "mediatype" string + a [files] section of "<vfs>" =
// "<relpath>" pairs + optional "[mediatypes]" overrides.
func loadDescriptor(path string) (*descriptor, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		return parseJSONDescriptor(body)
	}
	return parseTOMLDescriptor(body)
}

// jsonDescriptor matches the .json shape:
//
//	{
//	  "mediatype": "...",
//	  "files":      { "app.wasm": "terminal.wasm", ... },
//	  "mediatypes": { "app.wasm": "application/wasm" }
//	}
type jsonDescriptor struct {
	MediaType  string            `json:"mediatype"`
	Files      map[string]string `json:"files"`
	MediaTypes map[string]string `json:"mediatypes"`
}

func parseJSONDescriptor(body []byte) (*descriptor, error) {
	var j jsonDescriptor
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, fmt.Errorf("decode manifest JSON: %w", err)
	}
	return &descriptor{
		MediaType: j.MediaType,
		Files:     j.Files,
		PerFile:   j.MediaTypes,
	}, nil
}

// parseTOMLDescriptor reads the minimal TOML grammar this CLI
// supports. Lines are scanned left-to-right; the current section
// (set by `[name]`) determines which map a `k = "v"` line populates.
// Anything outside the documented surface returns an error rather
// than being silently dropped -- callers should know if their input
// is being ignored.
func parseTOMLDescriptor(body []byte) (*descriptor, error) {
	d := &descriptor{
		Files:   map[string]string{},
		PerFile: map[string]string{},
	}
	section := ""
	for lineno, raw := range strings.Split(string(body), "\n") {
		line := stripComment(strings.TrimSpace(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section != "files" && section != "mediatypes" {
				return nil, fmt.Errorf("line %d: unknown section %q", lineno+1, section)
			}
			continue
		}
		k, v, err := parseKV(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineno+1, err)
		}
		switch section {
		case "":
			if k != "mediatype" {
				return nil, fmt.Errorf("line %d: unknown top-level key %q", lineno+1, k)
			}
			d.MediaType = v
		case "files":
			d.Files[k] = v
		case "mediatypes":
			d.PerFile[k] = v
		}
	}
	return d, nil
}

// stripComment trims a trailing "# ..." comment. A "#" inside a
// quoted value is preserved (the simple state machine tracks one
// level of double-quote nesting).
func stripComment(s string) string {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}

// parseKV splits a `key = "value"` line. Both bare keys and
// "quoted keys" are accepted; the value MUST be double-quoted to
// keep the grammar small.
func parseKV(line string) (string, string, error) {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return "", "", fmt.Errorf("missing '=' in %q", line)
	}
	key := strings.TrimSpace(line[:eq])
	val := strings.TrimSpace(line[eq+1:])
	if len(key) >= 2 && key[0] == '"' && key[len(key)-1] == '"' {
		key = key[1 : len(key)-1]
	}
	if len(val) < 2 || val[0] != '"' || val[len(val)-1] != '"' {
		return "", "", fmt.Errorf("value must be double-quoted in %q", line)
	}
	return key, val[1 : len(val)-1], nil
}
