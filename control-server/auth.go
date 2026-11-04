package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"slices"
	"strings"
)

// validRoutes lists every route served, mirroring gluetun's control server,
// plus the NordVPN specific /v1/nordvpn/* routes.
var validRoutes = map[string][]string{
	// gluetun v0 (legacy) routes
	"/openvpn/actions/restart": {http.MethodGet},
	"/openvpn/portforwarded":   {http.MethodGet},
	"/openvpn/settings":        {http.MethodGet},
	"/unbound/actions/restart": {http.MethodGet},
	"/updater/restart":         {http.MethodGet},
	// gluetun v1 routes
	"/v1/version":               {http.MethodGet},
	"/v1/vpn/status":            {http.MethodGet, http.MethodPut},
	"/v1/vpn/settings":          {http.MethodGet, http.MethodPut},
	"/v1/openvpn/status":        {http.MethodGet, http.MethodPut},
	"/v1/openvpn/portforwarded": {http.MethodGet},
	"/v1/openvpn/settings":      {http.MethodGet},
	"/v1/dns/status":            {http.MethodGet, http.MethodPut},
	"/v1/updater/status":        {http.MethodGet, http.MethodPut},
	"/v1/publicip/ip":           {http.MethodGet},
	"/v1/portforward":           {http.MethodGet, http.MethodPut},
	// NordVPN specific routes
	"/v1/nordvpn/status":   {http.MethodGet},
	"/v1/nordvpn/settings": {http.MethodGet},
	"/v1/nordvpn/account":  {http.MethodGet},
}

// newAuthMiddleware only lets through requests carrying the API key in the
// X-API-Key header, the same header gluetun and its clients use.
func newAuthMiddleware(apiKey string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(apiKey))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		methods, ok := validRoutes[path]
		if !ok {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		if !slices.Contains(methods, r.Method) {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		// Hash both sides so the comparison is constant time regardless of length.
		got := sha256.Sum256([]byte(r.Header.Get("X-API-Key")))
		if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		r.URL.Path = path
		next.ServeHTTP(w, r)
	})
}
