package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"hostlier/internal/model"
)

// collectorReadiness refuses to report a completed install until shared collection is provisioned.
func (a *installationRunner) collectorReadiness() error {
	// Local administration stays on the protected Unix socket.
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/run/hostlier/controller.sock")
	}}}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()
	return awaitCollector(ctx, client, "http://local/v1/status/snapshot", time.Second)
}

// awaitCollector allows startup retries while keeping installation outcome tied to observed readiness.
func awaitCollector(ctx context.Context, client *http.Client, address string, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastStatus := "controller status unavailable"
	// The controller may still be acquiring TLS, pulling its image or creating node Pods.
	for {
		// A reachable HTTP service alone is insufficient to claim collector installation.
		ready, status := collectorStatus(ctx, client, address)
		if ready {
			return nil
		}
		if status != "" {
			lastStatus = status
		}
		// Cancellation and deadline expiration stop readiness polling promptly.
		select {
		// Return a safe diagnostic without leaking runtime output or credentials.
		case <-ctx.Done():
			return model.Err("COLLECTOR_NOT_READY", "Minimal collector is not ready: "+lastStatus+". Inspect the controller status and Docker collector, then retry init")
		// Retry after the configured interval instead of spinning on the local socket.
		case <-ticker.C:
		}
	}
}

// collectorStatus reads only bounded, nonsecret health metadata from the private controller endpoint.
func collectorStatus(ctx context.Context, client *http.Client, address string) (bool, string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	// Invalid transport configuration cannot produce a ready result.
	if err != nil {
		return false, "controller status request is invalid"
	}
	response, err := client.Do(request)
	// Startup connection failures are retried by the bounded caller.
	if err != nil {
		return false, "controller status unavailable on its local socket"
	}
	defer response.Body.Close()
	// Unauthorized and transient HTTP errors never imply successful provisioning.
	if response.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("controller status returned HTTP %d", response.StatusCode)
	}
	var snapshot struct {
		Telemetry *struct {
			Managed *struct {
				State  string `json:"state"`
				Reason string `json:"reason"`
			} `json:"managed"`
		} `json:"telemetry"`
	}
	// Missing or malformed metadata is not a positive collector observation.
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&snapshot) != nil || snapshot.Telemetry == nil || snapshot.Telemetry.Managed == nil {
		return false, "managed collector status missing from controller snapshot"
	}
	state := boundedCollectorStatus(snapshot.Telemetry.Managed.State, 40)
	if state == "ready" {
		return true, ""
	}
	if state == "" {
		state = "unknown"
	}
	status := "managed collector " + state
	if reason := boundedCollectorStatus(snapshot.Telemetry.Managed.Reason, 200); reason != "" {
		status += ": " + reason
	}
	return false, status
}

// boundedCollectorStatus removes control characters and limits diagnostic text from the status API.
func boundedCollectorStatus(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) || unicode.IsSpace(r) {
			return r
		}
		return -1
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}
