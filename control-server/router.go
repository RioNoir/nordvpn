package main

import (
	"encoding/json"
	"log"
	"net/http"
)

type statusWrapper struct {
	Status string `json:"status"`
}

type outcomeWrapper struct {
	Outcome string `json:"outcome"`
}

type portWrapper struct {
	Port uint16 `json:"port"`
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func notSupported(w http.ResponseWriter, what string) {
	http.Error(w, what+" is not supported by the NordVPN client", http.StatusNotImplemented)
}

func newRouter(logger *log.Logger, vpn *vpnController, publicIP *publicIPFetcher) http.Handler {
	mux := http.NewServeMux()

	getVPNStatus := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, statusWrapper{Status: vpn.Status(r.Context())})
	}
	putVPNStatus := func(w http.ResponseWriter, r *http.Request) {
		var body statusWrapper
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		outcome, err := vpn.ApplyStatus(r.Context(), body.Status)
		if err != nil {
			logger.Printf("applying vpn status %q: %s", body.Status, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, outcomeWrapper{Outcome: outcome})
	}
	getSettings := func(w http.ResponseWriter, r *http.Request) {
		settings, err := vpn.Settings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, settings)
	}
	getPortForwarded := func(w http.ResponseWriter, _ *http.Request) {
		// NordVPN only offers port forwarding on dedicated servers, and the
		// CLI does not report the forwarded port: 0 means "none", like gluetun.
		writeJSON(w, portWrapper{Port: 0})
	}

	// VPN
	mux.HandleFunc("GET /v1/vpn/status", getVPNStatus)
	mux.HandleFunc("PUT /v1/vpn/status", putVPNStatus)
	mux.HandleFunc("GET /v1/openvpn/status", getVPNStatus)
	mux.HandleFunc("PUT /v1/openvpn/status", putVPNStatus)
	mux.HandleFunc("GET /v1/vpn/settings", getSettings)
	mux.HandleFunc("GET /v1/openvpn/settings", getSettings)
	mux.HandleFunc("GET /openvpn/settings", getSettings)
	mux.HandleFunc("PUT /v1/vpn/settings", func(w http.ResponseWriter, _ *http.Request) {
		notSupported(w, "changing settings through the API")
	})
	mux.HandleFunc("GET /openvpn/actions/restart", func(w http.ResponseWriter, r *http.Request) {
		if _, err := vpn.ApplyStatus(r.Context(), statusStopped); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := vpn.ApplyStatus(r.Context(), statusRunning); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("openvpn restarted\n"))
	})

	// Public IP
	mux.HandleFunc("GET /v1/publicip/ip", func(w http.ResponseWriter, r *http.Request) {
		info, err := publicIP.Get(r.Context(), vpn)
		if err != nil {
			logger.Printf("getting public ip: %s", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, info)
	})

	// Port forwarding
	mux.HandleFunc("GET /v1/portforward", getPortForwarded)
	mux.HandleFunc("GET /v1/openvpn/portforwarded", getPortForwarded)
	mux.HandleFunc("GET /openvpn/portforwarded", getPortForwarded)
	mux.HandleFunc("PUT /v1/portforward", func(w http.ResponseWriter, _ *http.Request) {
		notSupported(w, "port forwarding")
	})

	// DNS and updater are handled by the NordVPN client itself.
	mux.HandleFunc("GET /v1/dns/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, statusWrapper{Status: statusRunning})
	})
	mux.HandleFunc("PUT /v1/dns/status", func(w http.ResponseWriter, _ *http.Request) {
		notSupported(w, "controlling DNS")
	})
	mux.HandleFunc("GET /v1/updater/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, statusWrapper{Status: "completed"})
	})
	mux.HandleFunc("PUT /v1/updater/status", func(w http.ResponseWriter, _ *http.Request) {
		notSupported(w, "the servers updater")
	})
	mux.HandleFunc("GET /unbound/actions/restart", func(w http.ResponseWriter, _ *http.Request) {
		notSupported(w, "unbound")
	})
	mux.HandleFunc("GET /updater/restart", func(w http.ResponseWriter, _ *http.Request) {
		notSupported(w, "the servers updater")
	})

	// Version
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{
			"version":         version,
			"commit":          commit,
			"created":         created,
			"nordvpn_version": vpn.Version(r.Context()),
		})
	})

	// NordVPN specific
	mux.HandleFunc("GET /v1/nordvpn/status", func(w http.ResponseWriter, r *http.Request) {
		status, err := vpn.nordStatus(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, status)
	})
	mux.HandleFunc("GET /v1/nordvpn/settings", getSettings)
	mux.HandleFunc("GET /v1/nordvpn/account", func(w http.ResponseWriter, r *http.Request) {
		account, err := vpn.Account(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, account)
	})

	return mux
}
