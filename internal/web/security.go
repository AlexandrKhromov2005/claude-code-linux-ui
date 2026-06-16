package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// newToken returns a random per-session bearer token.
func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// rand.Read failing is fatal for a security token.
		panic("web: cannot read random token: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// tokenEqual compares tokens in constant time.
func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// allowlist holds the host:port and origin values that requests may carry. It
// is derived from the loopback listen address, so anything else (a rebind
// domain, another origin) is rejected.
type allowlist struct {
	hosts   map[string]bool
	origins map[string]bool
}

func newAllowlist(port string) allowlist {
	hosts := map[string]bool{
		"127.0.0.1:" + port: true,
		"localhost:" + port: true,
		"[::1]:" + port:     true,
	}
	origins := map[string]bool{
		"http://127.0.0.1:" + port: true,
		"http://localhost:" + port: true,
		"http://[::1]:" + port:     true,
	}
	return allowlist{hosts: hosts, origins: origins}
}

// checkHostOrigin enforces the loopback Host and Origin allowlist. The Host
// header must always match (DNS-rebinding defense); the Origin header, when
// present, must match too (cross-site defense). It returns "" when allowed or a
// reason string otherwise.
func (a allowlist) checkHostOrigin(r *http.Request) string {
	if !a.hosts[r.Host] {
		return "host not allowed"
	}
	if origin := r.Header.Get("Origin"); origin != "" && !a.origins[strings.TrimRight(origin, "/")] {
		return "origin not allowed"
	}
	return ""
}

// bearerToken extracts the token from an Authorization: Bearer header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if strings.HasPrefix(h, p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}
