// Command control-server exposes an HTTP API compatible with the gluetun
// control server (https://github.com/qdm12/gluetun-wiki/blob/main/setup/advanced/control-server.md)
// on top of the official NordVPN CLI.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	address := envOr("HTTP_CONTROL_SERVER_ADDRESS", ":8000")
	logRequests := envBool("HTTP_CONTROL_SERVER_LOG", true)

	authSettings, err := loadAuthSettings(
		envOr("HTTP_CONTROL_SERVER_AUTH_CONFIG_FILEPATH", "/gluetun/auth/config.toml"),
		os.Getenv("HTTP_CONTROL_SERVER_AUTH_DEFAULT_ROLE"),
	)
	if err != nil {
		logger.Fatalf("loading authentication settings: %s", err)
	}
	if len(authSettings.Roles) == 0 {
		logger.Println("WARNING: no authentication role configured, every route will answer 401. " +
			"Set HTTP_CONTROL_SERVER_AUTH_DEFAULT_ROLE, for example {\"auth\":\"apikey\",\"apikey\":\"...\"}")
	}
	for _, role := range authSettings.Roles {
		logger.Printf("role %q: auth %s, %d routes", role.Name, role.Auth, len(role.Routes))
	}

	vpn := newVPN(logger)
	publicIP := newPublicIPFetcher(
		envBool("PUBLICIP_ENABLED", true),
		envOr("PUBLICIP_API", "nordvpn"),
		os.Getenv("PUBLICIP_API_TOKEN"),
	)

	var handler http.Handler = newRouter(logger, vpn, publicIP)
	handler, err = newAuthMiddleware(authSettings, handler)
	if err != nil {
		logger.Fatalf("creating authentication middleware: %s", err)
	}
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
