// Command control-server exposes an HTTP API compatible with the gluetun
// control server (https://github.com/qdm12/gluetun-wiki/blob/main/setup/advanced/control-server.md)
// on top of the official NordVPN CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Set at build time with -ldflags.
var (
	version = "unknown"
	commit  = "unknown"
	created = "unknown"
)

func main() {
	logger := log.New(os.Stdout, "[control-server] ", 0)

	address := ":" + envOr("CONTROL_API_PORT", "8000")
	logRequests := envBool("CONTROL_API_LOG", true)

	apiKey, err := loadAPIKey(os.Getenv("CONTROL_API_KEY"), os.Getenv("CONTROL_API_KEY_FILE"))
	if err != nil {
		logger.Fatal(err)
	}
	if len(apiKey) < minAPIKeyLength {
		logger.Printf("WARNING: CONTROL_API_KEY is shorter than %d characters, use a longer random key", minAPIKeyLength)
	}

	vpn := newVPN(logger)
	publicIP := newPublicIPFetcher(
		envBool("PUBLICIP_ENABLED", true),
		envOr("PUBLICIP_API", "nordvpn"),
		os.Getenv("PUBLICIP_API_TOKEN"),
	)

	handler := newAuthMiddleware(apiKey, newRouter(logger, vpn, publicIP))
	if logRequests {
		handler = logMiddleware(logger, handler)
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Printf("listening on %s", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("http server: %s", err)
	}
}

const minAPIKeyLength = 16

// loadAPIKey returns CONTROL_API_KEY, or the first line of CONTROL_API_KEY_FILE
// (for Docker secrets) when the variable is empty.
func loadAPIKey(key, keyFile string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" && keyFile != "" {
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return "", fmt.Errorf("reading CONTROL_API_KEY_FILE: %w", err)
		}
		key, _, _ = strings.Cut(string(data), "\n")
		key = strings.TrimSpace(key)
	}
	if key == "" {
		return "", errors.New("no API key: set CONTROL_API_KEY or CONTROL_API_KEY_FILE")
	}
	return key, nil
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch os.Getenv(key) {
	case "1", "on", "true", "yes", "enable", "enabled", "ON", "True", "TRUE", "Yes", "YES":
		return true
	case "0", "off", "false", "no", "disable", "disabled", "OFF", "False", "FALSE", "No", "NO":
		return false
	}
	return fallback
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func logMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		logger.Printf("%s %s %d from %s in %s", r.Method, r.URL.Path, recorder.status,
			r.RemoteAddr, time.Since(start).Round(time.Millisecond))
	})
}
