package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// stoppedMarker tells nord_watch and the healthcheck that the VPN was
// stopped on purpose through the API and must not be reconnected.
const stoppedMarker = "/run/nordvpn-control/stopped"

// gluetun VPN statuses
const (
	statusStarting = "starting"
	statusRunning  = "running"
	statusStopping = "stopping"
	statusStopped  = "stopped"
	statusCrashed  = "crashed"
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// cleanOutput removes ANSI colors and the spinner the NordVPN CLI prints
// ("\r-\r  \r") before the actual output.
func cleanOutput(raw string) string {
	raw = ansiEscape.ReplaceAllString(raw, "")
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for i, line := range lines {
		if idx := strings.LastIndex(line, "\r"); idx >= 0 {
			line = line[idx+1:]
		}
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

func runNordVPN(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nordvpn", args...).CombinedOutput()
	cleaned := cleanOutput(string(out))
	if err != nil {
		return cleaned, fmt.Errorf("nordvpn %s: %w: %s", strings.Join(args, " "), err, cleaned)
	}
	return cleaned, nil
}

var nonKeyChars = regexp.MustCompile(`[^a-z0-9]+`)

func toKey(label string) string {
	return strings.Trim(nonKeyChars.ReplaceAllString(strings.ToLower(label), "_"), "_")
}

// parseKeyValues turns "Key: value" CLI output into a map with snake_case
// keys. A "Key:" line without value followed by indented lines becomes a list.
func parseKeyValues(output string) map[string]any {
	result := map[string]any{}
	lastKey := ""
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indented := line != strings.TrimLeft(line, " \t")
		if indented && lastKey != "" {
			if list, ok := result[lastKey].([]string); ok {
				result[lastKey] = append(list, trimmed)
				continue
			}
		}
		label, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key := toKey(label)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			result[key] = []string{}
		} else {
			result[key] = value
		}
		lastKey = key
	}
	return result
}

type vpnController struct {
	logger *log.Logger
	mutex  sync.Mutex
	// transition holds "starting"/"stopping" while a PUT is in progress.
	transition string
}

func newVPN(logger *log.Logger) *vpnController {
	return &vpnController{logger: logger}
}

func (v *vpnController) nordStatus(ctx context.Context) (map[string]any, error) {
	out, err := runNordVPN(ctx, "status")
	if err != nil {
		return nil, err
	}
	return parseKeyValues(out), nil
}

// Status returns the VPN status using gluetun's vocabulary.
func (v *vpnController) Status(ctx context.Context) string {
	v.mutex.Lock()
	transition := v.transition
	v.mutex.Unlock()
	if transition != "" {
		return transition
	}

	status, err := v.nordStatus(ctx)
	if err != nil {
		return statusCrashed
	}
	state, _ := status["status"].(string)
	switch strings.ToLower(state) {
	case "connected":
		return statusRunning
	case "connecting", "reconnecting":
		return statusStarting
	case "disconnecting":
		return statusStopping
	default:
		return statusStopped
	}
}

var errUnknownStatus = errors.New(`status must be "running" or "stopped"`)

// ApplyStatus starts or stops the VPN and returns a gluetun-like outcome.
func (v *vpnController) ApplyStatus(ctx context.Context, wanted string) (string, error) {
	if wanted != statusRunning && wanted != statusStopped {
		return "", errUnknownStatus
	}

	current := v.Status(ctx)
	if current == wanted {
		return "already " + wanted, nil
	}

	v.mutex.Lock()
	if v.transition != "" {
		busy := v.transition
		v.mutex.Unlock()
		return "", fmt.Errorf("vpn is already %s", busy)
	}
	if wanted == statusRunning {
		v.transition = statusStarting
	} else {
		v.transition = statusStopping
	}
	v.mutex.Unlock()
	defer func() {
		v.mutex.Lock()
		v.transition = ""
		v.mutex.Unlock()
	}()

	if wanted == statusStopped {
		if err := os.MkdirAll("/run/nordvpn-control", 0o755); err == nil {
			_ = os.WriteFile(stoppedMarker, nil, 0o644)
		}
		if _, err := runNordVPN(ctx, "disconnect"); err != nil {
			return "", err
		}
		v.logger.Println("vpn stopped through the API")
		return statusStopped, nil
	}

	_ = os.Remove(stoppedMarker)
	// nord_connect runs PRE_CONNECT/POST_CONNECT and retries with backoff;
	// give up after a few minutes and let nord_watch take over.
	connectCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(connectCtx, "nord_connect").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("connecting: %w: %s", err, cleanOutput(string(out)))
	}
	v.logger.Println("vpn started through the API")
	return statusRunning, nil
}

func (v *vpnController) Settings(ctx context.Context) (map[string]any, error) {
	out, err := runNordVPN(ctx, "settings")
	if err != nil {
		return nil, err
	}
	return parseKeyValues(out), nil
}

func (v *vpnController) Account(ctx context.Context) (map[string]any, error) {
	out, err := runNordVPN(ctx, "account")
	if err != nil {
		return nil, err
	}
	return parseKeyValues(out), nil
}

func (v *vpnController) Version(ctx context.Context) string {
	out, err := runNordVPN(ctx, "version")
	if err != nil {
		return ""
	}
	// "NordVPN Version 6.0.2"
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}
