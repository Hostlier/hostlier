package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealCollectorPayloads validates JSON from actual Docker and k3s collector executions.
func TestRealCollectorPayloads(t *testing.T) {
	// Exercise each independent fixture case with the same bounded-store contract.
	for _, runtime := range []string{"docker", "k3s"} {
		// Exercise each independent fixture case with the same bounded-store contract.
		for _, signal := range []string{"metrics", "logs"} {
			// Keep this fixture callback isolated from production authority and clocks.
			t.Run(runtime+"-"+signal, func(t *testing.T) {
				s := testStore(t)
				// Keep this fixture callback isolated from production authority and clocks.
				s.now = func() time.Time { return time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC) }
				data, err := os.ReadFile(filepath.Join("..", "collector", "testdata", runtime+"-"+signal+".json"))
				// Fail immediately when the observed result violates this fixture contract.
				if err != nil {
					t.Fatal(err)
				}
				result, err := s.Ingest(context.Background(), signal, data, Scope{"fixture", runtime}, resolveCollectorFixture)
				// Fail immediately when the observed result violates this fixture contract.
				if err != nil || result.Accepted != map[string]int{"docker-metrics": 12, "docker-logs": 1, "k3s-metrics": 31, "k3s-logs": 10}[runtime+"-"+signal] || result.Rejected != 0 {
					t.Fatal(result, err)
				}
				page, err := s.Query(context.Background(), Query{})
				// Preserve the identity verified by the fixture's owner resolver.
				if err != nil || len(page.Records) == 0 || page.Records[0].Identity.Application != "fixture" {
					t.Fatal(page, err)
				}
			})
		}
	}
}

// TestStructuralLimitsRejectEmptyFlood bounds work independently of sample counts.
func TestStructuralLimitsRejectEmptyFlood(t *testing.T) {
	s := testStore(t)
	data := []byte(`{"resourceLogs":[` + strings.Repeat(`{},`, 8193) + `{}]}`)
	// Fail immediately when the observed result violates this fixture contract.
	if _, err := s.Ingest(context.Background(), "logs", data, Scope{"c", "docker"}, resolveFixture); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

// TestCanceledIngressDoesNotPersist aborts the bounded write path after cancellation.
func TestCanceledIngressDoesNotPersist(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Fail immediately when the observed result violates this fixture contract.
	if _, err := s.Ingest(ctx, "logs", logBody(s.now()), Scope{"c", "docker"}, resolveFixture); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// resolveCollectorFixture validates concrete runtime identities from real collector output.
func resolveCollectorFixture(r Resource) (Identity, error) {
	// Fail immediately when the observed result violates this fixture contract.
	if id := r["container.id"]; id != "" {
		// Fail immediately when the observed result violates this fixture contract.
		if id != strings.Repeat("a", 64) {
			return Identity{}, errors.New("unexpected container ID")
		}
		return Identity{Application: "fixture", Instance: id}, nil
	}
	// Fail immediately when the observed result violates this fixture contract.
	if r["k8s.pod.uid"] != "" {
		// Bind the fixture to its sanitized stable Kubernetes UID and node.
		if r["k8s.pod.uid"] != "11111111-2222-3333-4444-555555555555" || r["k8s.node.name"] != "fixture-node" {
			return Identity{}, errors.New("unexpected pod or node")
		}
		return Identity{Application: "fixture", Pod: r["k8s.pod.uid"], Component: r["k8s.container.name"], Node: r["k8s.node.name"]}, nil
	}
	// Fail immediately when the observed result violates this fixture contract.
	if r["k8s.node.name"] != "" {
		return Identity{Application: "fixture", Node: r["k8s.node.name"]}, nil
	}
	return Identity{}, errors.New("missing fixture runtime identity")
}
