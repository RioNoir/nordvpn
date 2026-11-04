package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
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

const (
	authNone   = "none"
	authAPIKey = "apikey"
	authBasic  = "basic"
)

type authRole struct {
	Name     string   `json:"name" toml:"name"`
	Auth     string   `json:"auth" toml:"auth"`
	APIKey   string   `json:"apikey" toml:"apikey"`
	Username string   `json:"username" toml:"username"`
	Password string   `json:"password" toml:"password"`
	Routes   []string `json:"-" toml:"routes"`
}

type authSettings struct {
	Roles []authRole `toml:"roles"`
}

func (r authRole) validate() error {
	switch r.Auth {
	case authNone, authAPIKey, authBasic:
	default:
		return fmt.Errorf("authentication method not supported: %q", r.Auth)
	}
	switch {
	case r.Auth == authAPIKey && r.APIKey == "":
		return fmt.Errorf("role %s: api key is empty", r.Name)
	case r.Auth == authBasic && (r.Username == "" || r.Password == ""):
		return fmt.Errorf("role %s: username or password is empty", r.Name)
	}
	for _, route := range r.Routes {
		method, path, ok := strings.Cut(route, " ")
		if !ok {
			return fmt.Errorf("role %s: route %q must be in the form \"METHOD /path\"", r.Name, route)
		}
		methods, known := validRoutes[path]
		if !known {
			return fmt.Errorf("role %s: route path not supported: %s", r.Name, path)
		}
		if !slices.Contains(methods, method) {
			return fmt.Errorf("role %s: method %s not supported for path %s", r.Name, method, path)
		}
	}
	return nil
}

// loadAuthSettings reads the optional TOML config file, then applies the
// default role (JSON) to every route not covered by the file, like gluetun.
func loadAuthSettings(configPath, defaultRoleJSON string) (settings authSettings, err error) {
	data, err := os.ReadFile(configPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return settings, fmt.Errorf("reading %s: %w", configPath, err)
	default:
		decoder := toml.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&settings); err != nil {
			return settings, fmt.Errorf("decoding %s: %w", configPath, err)
		}
	}

	for _, role := range settings.Roles {
		if err := role.validate(); err != nil {
			return settings, err
		}
	}

	if strings.TrimSpace(defaultRoleJSON) == "" {
		return settings, nil
	}
	var role authRole
	decoder := json.NewDecoder(strings.NewReader(defaultRoleJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&role); err != nil {
		return settings, fmt.Errorf("decoding HTTP_CONTROL_SERVER_AUTH_DEFAULT_ROLE: %w", err)
	}
	if role.Auth == "" {
		return settings, nil
	}
	if role.Name == "" {
		role.Name = "default"
	}

	covered := map[string]bool{}
	for _, r := range settings.Roles {
		for _, route := range r.Routes {
			covered[route] = true
		}
	}
	for path, methods := range validRoutes {
		for _, method := range methods {
			if route := method + " " + path; !covered[route] {
				role.Routes = append(role.Routes, route)
			}
		}
	}
	slices.Sort(role.Routes)
	if err := role.validate(); err != nil {
		return settings, fmt.Errorf("default role: %w", err)
	}
	settings.Roles = append(settings.Roles, role)
	return settings, nil
}

type authChecker func(header http.Header, r *http.Request) bool

func newChecker(role authRole) authChecker {
	switch role.Auth {
	case authAPIKey:
		want := sha256.Sum256([]byte(role.APIKey))
		return func(_ http.Header, r *http.Request) bool {
			got := sha256.Sum256([]byte(r.Header.Get("X-API-Key")))
			return subtle.ConstantTimeCompare(want[:], got[:]) == 1
		}
	case authBasic:
		wantUser := sha256.Sum256([]byte(role.Username))
		wantPass := sha256.Sum256([]byte(role.Password))
		return func(header http.Header, r *http.Request) bool {
			user, pass, ok := r.BasicAuth()
			if !ok {
				header.Set("WWW-Authenticate", `Basic realm="restricted", charset="UTF-8"`)
				return false
			}
			gotUser := sha256.Sum256([]byte(user))
			gotPass := sha256.Sum256([]byte(pass))
			return subtle.ConstantTimeCompare(wantUser[:], gotUser[:])&
				subtle.ConstantTimeCompare(wantPass[:], gotPass[:]) == 1
		}
	default:
		return func(http.Header, *http.Request) bool { return true }
	}
}

func newAuthMiddleware(settings authSettings, next http.Handler) (http.Handler, error) {
	routeCheckers := map[string][]authChecker{}
	for _, role := range settings.Roles {
		checker := newChecker(role)
		for _, route := range role.Routes {
			routeCheckers[route] = append(routeCheckers[route], checker)
		}
	}

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

		responseHeader := http.Header{}
		for _, checker := range routeCheckers[r.Method+" "+path] {
			if checker(responseHeader, r) {
				r.URL.Path = path
				next.ServeHTTP(w, r)
				return
			}
		}
		for key, values := range responseHeader {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
	}), nil
}
