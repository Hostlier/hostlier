package installer

import (
	"os"
	"strings"
	"testing"
)

// TestOwnedDocker29Ingress preserves real loopback publications while rejecting broader restrictions.
func TestOwnedDocker29Ingress(t *testing.T) {
	raw, err := os.ReadFile("testdata/docker29-nft.json")
	// Stop the check when its prerequisite operation fails.
	if err != nil {
		t.Fatal(err)
	}
	// Stop the check when its prerequisite operation fails.
	if blocked, err := nftInputFiltering(raw, false, true); blocked || err != nil {
		t.Fatal(blocked, err)
	}
	// A package alone or arbitrary chain names cannot authorize compatibility rules.
	if blocked, _ := nftInputFiltering(raw); !blocked {
		t.Fatal("unowned Docker firewall accepted")
	}
	cases := map[string]string{
		"public destination": strings.ReplaceAll(string(raw), "172.18.0.2", "203.0.113.2"),
		"public web port":    strings.ReplaceAll(string(raw), "41983", "443"),
		"unknown bridge":     strings.ReplaceAll(string(raw), "br-c2240de43604", "eth0"),
	}
	// Each mutation broadens the recognized rule beyond its exact private publication shape.
	for name, rules := range cases {
		// Require this independently broadened ingress variant to remain rejected.
		t.Run(name, func(t *testing.T) {
			// Require the exact validated scope before accepting this branch.
			if blocked, _ := nftInputFiltering([]byte(rules), false, true); !blocked {
				t.Fatal("unsafe ingress accepted")
			}
		})
	}
}

// TestOwnedDockerStillRejectsUnrelatedIngress keeps ordinary host firewall restrictions visible.
func TestOwnedDockerStillRejectsUnrelatedIngress(t *testing.T) {
	raw, err := os.ReadFile("testdata/docker29-nft.json")
	// Stop the check when its prerequisite operation fails.
	if err != nil {
		t.Fatal(err)
	}
	// Insert an ordinary INPUT restriction alongside otherwise valid Docker rules.
	extra := `,{"chain":{"family":"ip","table":"filter","name":"INPUT","hook":"input","policy":"drop"}}`
	rules := strings.TrimSuffix(strings.TrimSpace(string(raw)), "]}") + extra + "]}"
	// Require the exact validated scope before accepting this branch.
	if blocked, _ := nftInputFiltering([]byte(rules), false, true); !blocked {
		t.Fatal("host INPUT drop hidden")
	}
}

// TestOwnedDockerNATJumpStillInspectsTarget rejects restrictions hidden behind a recognized entry.
func TestOwnedDockerNATJumpStillInspectsTarget(t *testing.T) {
	raw, err := os.ReadFile("testdata/docker29-nft.json")
	// Stop the check when its prerequisite operation fails.
	if err != nil {
		t.Fatal(err)
	}
	// A legitimate entry chain cannot authorize an arbitrary rule in its target.
	extra := `,{"rule":{"family":"ip","table":"nat","chain":"DOCKER","expr":[{"drop":null}]}}`
	rules := strings.TrimSuffix(strings.TrimSpace(string(raw)), "]}") + extra + "]}"
	// Require the exact validated scope before accepting this branch.
	if blocked, _ := nftInputFiltering([]byte(rules), false, true); !blocked {
		t.Fatal("Docker target drop hidden")
	}
}
