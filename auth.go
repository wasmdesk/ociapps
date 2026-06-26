// Copyright (c) 2026 the wasmdesk/ociapps authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociapps

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// httpNewRequest is a test seam: fetchToken's NewRequest call cannot fail for a
// URL that already round-tripped through url.Parse + url.String, so the error
// branch is only reachable by swapping this out.
var httpNewRequest = http.NewRequest

// TokenAuthDoer wraps an [HTTPDoer] with OCI Distribution / Docker-registry
// Bearer-token auth. Registries such as ghcr.io answer an unauthenticated
// request with 401 + a `WWW-Authenticate: Bearer realm=...,service=...,
// scope=...` challenge; this fetches a token from the realm (anonymously, or
// with the configured credentials for push scopes) and retries the original
// request with `Authorization: Bearer <token>`. The last token is reused across
// subsequent requests (a push is single-repo, so its many blob uploads do not
// each re-auth); a stale token simply triggers one more challenge + refresh.
//
// Empty Username+Password is valid — it does anonymous token fetches, which is
// all a public registry needs for read. A ghcr **push** needs a GitHub username
// plus a PAT / GITHUB_TOKEN with `packages:write`.
//
// It is safe for sequential use (the pusher uploads blobs one at a time); the
// token cache is mutex-guarded so a shared doer does not race.
type TokenAuthDoer struct {
	// Base is the underlying doer (nil -> http.DefaultClient).
	Base HTTPDoer
	// Username / Password are sent as HTTP Basic auth to the token realm.
	Username string
	Password string

	mu    sync.Mutex
	token string
}

func (t *TokenAuthDoer) base() HTTPDoer {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultClient
}

func (t *TokenAuthDoer) cached() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.token
}

func (t *TokenAuthDoer) store(tok string) {
	t.mu.Lock()
	t.token = tok
	t.mu.Unlock()
}

// Do sends req, transparently handling a single Bearer-token challenge: it
// attaches any cached token, and on a 401 with a parseable Bearer challenge it
// fetches a fresh token and replays the request once.
func (t *TokenAuthDoer) Do(req *http.Request) (*http.Response, error) {
	if tok := t.cached(); tok != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := t.base().Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	realm, service, scope, ok := parseBearerChallenge(resp.Header.Get("WWW-Authenticate"))
	if !ok {
		// Not a Bearer challenge we understand — surface the 401 unchanged.
		return resp, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	tok, err := t.fetchToken(realm, service, scope)
	if err != nil {
		return nil, err
	}
	t.store(tok)

	// Replay with the token. http.NewRequest sets GetBody for the in-memory
	// bodies the pushers use (bytes.Reader), so the upload body is replayable.
	retry := req.Clone(req.Context())
	if req.Body != nil && req.GetBody != nil {
		body, gerr := req.GetBody()
		if gerr != nil {
			return nil, fmt.Errorf("ociapps: replay request body: %w", gerr)
		}
		retry.Body = body
	}
	retry.Header.Set("Authorization", "Bearer "+tok)
	return t.base().Do(retry)
}

// fetchToken GETs realm?service=&scope=, with Basic auth when credentials are
// set, and returns the bearer token from the JSON response.
func (t *TokenAuthDoer) fetchToken(realm, service, scope string) (string, error) {
	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("ociapps: bad token realm %q: %w", realm, err)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()

	req, err := httpNewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if t.Username != "" || t.Password != "" {
		req.Header.Set("Authorization", "Basic "+basicAuth(t.Username, t.Password))
	}
	resp, err := t.base().Do(req)
	if err != nil {
		return "", fmt.Errorf("ociapps: token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ociapps: token request: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("ociapps: decode token response: %w", err)
	}
	tok := tr.Token
	if tok == "" {
		tok = tr.AccessToken // some registries use the OAuth2 field name
	}
	if tok == "" {
		return "", fmt.Errorf("ociapps: token response carried no token")
	}
	return tok, nil
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// parseBearerChallenge extracts realm/service/scope from a
// `WWW-Authenticate: Bearer realm="...",service="...",scope="..."` header.
// ok is false for any non-Bearer header or one without a realm.
func parseBearerChallenge(header string) (realm, service, scope string, ok bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", "", "", false
	}
	for _, part := range splitParams(header[len(prefix):]) {
		k, v, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.TrimSpace(k) {
		case "realm":
			realm = v
		case "service":
			service = v
		case "scope":
			scope = v
		}
	}
	if realm == "" {
		return "", "", "", false
	}
	return realm, service, scope, true
}

// splitParams splits a challenge parameter list on commas that are not inside
// double quotes — scope values legitimately contain commas
// (`scope="repository:x:pull,push"`), so a naive Split would corrupt them.
func splitParams(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}
