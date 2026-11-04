package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseKeyValues(t *testing.T) {
	raw := "\r-\r  \r\x1b[32mStatus: Connected\x1b[0m\n" +
		"Server: Italy #123\n" +
		"Hostname: it123.nordvpn.com\n" +
		"IP: 1.2.3.4\n" +
		"VPN protocol: NordLynx\n" +
		"Uptime: 1 minute 2 seconds\n" +
		"Allowlisted subnets:\n" +
		"\t172.18.0.0/16\n" +
		"\t192.168.1.0/24\n" +
		"DNS: disabled\n"

	got := parseKeyValues(cleanOutput(raw))
	want := map[string]any{
		"status":              "Connected",
		"server":              "Italy #123",
		"hostname":            "it123.nordvpn.com",
		"ip":                  "1.2.3.4",
		"vpn_protocol":        "NordLynx",
		"uptime":              "1 minute 2 seconds",
		"allowlisted_subnets": []string{"172.18.0.0/16", "192.168.1.0/24"},
		"dns":                 "disabled",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseKeyValues()\n got: %#v\nwant: %#v", got, want)
	}
}

func serve(t *testing.T, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	recorder := httptest.NewRecorder()
	newAuthMiddleware("secret-key-1234567890", ok).ServeHTTP(recorder, request)
	return recorder
}

func TestAuthAPIKey(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		key    string
		want   int
	}{
		{"no key", http.MethodGet, "/v1/vpn/status", "", http.StatusUnauthorized},
		{"wrong key", http.MethodGet, "/v1/vpn/status", "wrong", http.StatusUnauthorized},
		{"right key", http.MethodGet, "/v1/vpn/status", "secret-key-1234567890", http.StatusOK},
		{"right key, PUT", http.MethodPut, "/v1/vpn/status", "secret-key-1234567890", http.StatusOK},
		{"trailing slash", http.MethodGet, "/v1/publicip/ip/", "secret-key-1234567890", http.StatusOK},
		{"unknown route", http.MethodGet, "/nope", "secret-key-1234567890", http.StatusNotFound},
		{"bad method", http.MethodDelete, "/v1/vpn/status", "secret-key-1234567890", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		request := httptest.NewRequest(c.method, c.path, nil)
		if c.key != "" {
			request.Header.Set("X-API-Key", c.key)
		}
		if got := serve(t, request).Code; got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLoadAPIKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("  from-file  \nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if key, err := loadAPIKey(" from-env ", file); err != nil || key != "from-env" {
		t.Errorf("env takes precedence: got %q, %v", key, err)
	}
	if key, err := loadAPIKey("", file); err != nil || key != "from-file" {
		t.Errorf("from file: got %q, %v", key, err)
	}
	if _, err := loadAPIKey("", ""); err == nil {
		t.Error("no key: expected an error")
	}
	if _, err := loadAPIKey("", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file: expected an error")
	}
}

func TestRoutesMatchAuthList(t *testing.T) {
	// Every route accepted by the auth middleware must be served by the router.
	router := newRouter(nil, newVPN(nil), newPublicIPFetcher(false, "", ""))
	mux := router.(*http.ServeMux)
	for path, methods := range validRoutes {
		for _, method := range methods {
			_, pattern := mux.Handler(httptest.NewRequest(method, path, nil))
			if pattern == "" {
				t.Errorf("route %s %s has no handler", method, path)
			}
		}
	}
}
