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

func serve(t *testing.T, settings authSettings, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler, err := newAuthMiddleware(settings, ok)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAuthNoRoleDeniesEverything(t *testing.T) {
	settings, err := loadAuthSettings(filepath.Join(t.TempDir(), "missing.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	got := serve(t, settings, httptest.NewRequest(http.MethodGet, "/v1/publicip/ip", nil))
	if got.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", got.Code)
	}
}

func TestAuthAPIKeyDefaultRole(t *testing.T) {
	settings, err := loadAuthSettings("/nonexistent", `{"auth":"apikey","apikey":"secret"}`)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/vpn/status", nil)
	if got := serve(t, settings, request); got.Code != http.StatusUnauthorized {
		t.Fatalf("without key: got %d, want 401", got.Code)
	}
	request.Header.Set("X-API-Key", "wrong")
	if got := serve(t, settings, request); got.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: got %d, want 401", got.Code)
	}
	request.Header.Set("X-API-Key", "secret")
	if got := serve(t, settings, request); got.Code != http.StatusOK {
		t.Fatalf("right key: got %d, want 200", got.Code)
	}
	trailing := httptest.NewRequest(http.MethodGet, "/v1/vpn/status/", nil)
	trailing.Header.Set("X-API-Key", "secret")
	if got := serve(t, settings, trailing); got.Code != http.StatusOK {
		t.Fatalf("trailing slash: got %d, want 200", got.Code)
	}
	if got := serve(t, settings, httptest.NewRequest(http.MethodGet, "/nope", nil)); got.Code != http.StatusNotFound {
		t.Fatalf("unknown route: got %d, want 404", got.Code)
	}
	if got := serve(t, settings, httptest.NewRequest(http.MethodDelete, "/v1/vpn/status", nil)); got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("bad method: got %d, want 405", got.Code)
	}
}

func TestAuthConfigFileTakesPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	config := `
[[roles]]
name = "homepage"
routes = ["GET /v1/publicip/ip"]
auth = "none"

[[roles]]
name = "admin"
routes = ["PUT /v1/vpn/status"]
auth = "basic"
username = "admin"
password = "pw"
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadAuthSettings(path, `{"auth":"apikey","apikey":"secret"}`)
	if err != nil {
		t.Fatal(err)
	}

	if got := serve(t, settings, httptest.NewRequest(http.MethodGet, "/v1/publicip/ip", nil)); got.Code != http.StatusOK {
		t.Fatalf("public route: got %d, want 200", got.Code)
	}

	put := httptest.NewRequest(http.MethodPut, "/v1/vpn/status", nil)
	put.Header.Set("X-API-Key", "secret")
	got := serve(t, settings, put)
	if got.Code != http.StatusUnauthorized {
		t.Fatalf("api key on basic-only route: got %d, want 401", got.Code)
	}
	if got.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate header")
	}
	put.SetBasicAuth("admin", "pw")
	if got := serve(t, settings, put); got.Code != http.StatusOK {
		t.Fatalf("basic auth: got %d, want 200", got.Code)
	}

	status := httptest.NewRequest(http.MethodGet, "/v1/vpn/status", nil)
	status.Header.Set("X-API-Key", "secret")
	if got := serve(t, settings, status); got.Code != http.StatusOK {
		t.Fatalf("default role route: got %d, want 200", got.Code)
	}
}

func TestAuthInvalidConfig(t *testing.T) {
	cases := map[string]string{
		"unknown method": `{"auth":"oauth"}`,
		"empty api key":  `{"auth":"apikey"}`,
		"unknown field":  `{"auth":"none","foo":1}`,
	}
	for name, role := range cases {
		if _, err := loadAuthSettings("/nonexistent", role); err == nil {
			t.Errorf("%s: expected an error", name)
		}
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
