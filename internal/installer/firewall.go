package installer

import (
	"encoding/json"
	"fmt"
	"strings"
)

// nftChain describes one table-scoped chain and its ingress policy.
type nftChain struct{ Family, Table, Name, Hook, Policy string }

// nftRule preserves expression order and the original ownership comment.
type nftRule struct {
	Comment              string
	Family, Table, Chain string
	Expr                 []map[string]json.RawMessage
}

// nftGraph owns the inspected ruleset and cycle detection during traversal.
type nftGraph struct {
	trusted bool
	docker  bool
	chains  map[string]nftChain
	rules   map[string][]nftRule
	starts  []string
	visited map[string]bool
}

// nftKey qualifies chain names by family and table to avoid cross-table adoption.
func nftKey(family, table, name string) string { return family + "/" + table + "/" + name }

// parseNFTGraph collects ingress hooks and their jump targets without mutating rules.
func parseNFTGraph(raw []byte, trusted bool, docker ...bool) (*nftGraph, error) {
	var document struct {
		NFTables []map[string]json.RawMessage `json:"nftables"`
	}
	// Reject malformed nft JSON rather than assuming an empty firewall.
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	// Require an actual ruleset collection in a decoded document.
	if document.NFTables == nil {
		return nil, fmt.Errorf("missing nftables ruleset")
	}
	graph := &nftGraph{trusted: trusted, chains: map[string]nftChain{}, rules: map[string][]nftRule{}, visited: map[string]bool{}}
	// Docker exceptions require an independently owned pinned installation.
	graph.docker = len(docker) > 0 && docker[0]
	// Collect every chain and rule before following ingress jumps.
	for _, entry := range document.NFTables {
		// A malformed entry invalidates the whole inspection.
		if err := graph.add(entry); err != nil {
			return nil, err
		}
	}
	return graph, nil
}

// add decodes chain and rule entries independently, retaining declaration order.
func (g *nftGraph) add(entry map[string]json.RawMessage) error {
	// Decode chain metadata when present in this entry.
	if data, ok := entry["chain"]; ok {
		var chain nftChain
		// Reject malformed chain fields before indexing ownership boundaries.
		if err := json.Unmarshal(data, &chain); err != nil {
			return err
		}
		key := nftKey(chain.Family, chain.Table, chain.Name)
		g.chains[key] = chain
		// Only host ingress hooks begin filtering analysis.
		if chain.Hook == "input" || chain.Hook == "prerouting" {
			g.starts = append(g.starts, key)
		}
	}
	// Decode rule metadata independently because an entry may carry either form.
	if data, ok := entry["rule"]; ok {
		var rule nftRule
		// Reject malformed rule expressions before trusting the ruleset.
		if err := json.Unmarshal(data, &rule); err != nil {
			return err
		}
		key := nftKey(rule.Family, rule.Table, rule.Chain)
		g.rules[key] = append(g.rules[key], rule)
	}
	return nil
}

// blocks walks reachable ingress chains once and rejects unknown targets or policies.
func (g *nftGraph) blocks(key string) (bool, error) {
	// Break chain cycles after their first complete traversal starts.
	if g.visited[key] {
		return false, nil
	}
	g.visited[key] = true
	chain, ok := g.chains[key]
	// An unresolved jump cannot be safely assumed permissive.
	if !ok {
		return true, fmt.Errorf("unknown nftables jump target")
	}
	// A non-accept base policy already filters host ingress.
	if chain.Policy != "" && chain.Policy != "accept" {
		return true, nil
	}
	// Inspect rules in declaration order.
	for _, rule := range g.rules[key] {
		// Skip only rules backed by the independently verified cluster ownership contract.
		if g.ownedRule(chain, rule) || g.ownedDockerRule(chain, rule) {
			continue
		}
		// Propagate unsafe verdicts and parser failures immediately.
		if blocked, err := g.ruleBlocks(chain, rule); blocked || err != nil {
			return blocked, err
		}
	}
	return false, nil
}

// ownedRule recognizes only verified compatibility inputs or owned management rules.
func (g *nftGraph) ownedRule(chain nftChain, rule nftRule) bool {
	// Without independent textual verification, no opaque cluster rule is trusted.
	if !g.trusted {
		return false
	}
	// Compatibility ownership applies only to host INPUT in the expected filter tables.
	if chain.Table == "filter" && chain.Name == "INPUT" && (chain.Family == "ip" || chain.Family == "ip6") {
		// Require the exact recognized compatibility expression forms.
		if knownCompatibilityInput(rule.Expr) {
			return true
		}
	}
	return strings.HasPrefix(rule.Comment, "hostlier-k3s-")
}

// ruleBlocks preserves expression order and stops at the first unsafe verdict.
func (g *nftGraph) ruleBlocks(chain nftChain, rule nftRule) (bool, error) {
	// A recognized NAT entry still traverses DOCKER so unrelated target rules cannot hide.
	if g.docker && chain.Table == "nat" && chain.Name == "PREROUTING" && dockerNATEntry(withoutCounters(rule.Expr)) {
		return g.blocks(nftKey(chain.Family, chain.Table, "DOCKER"))
	}
	// Preserve expression order within each rule.
	for _, expr := range rule.Expr {
		// Inspect every expression kind, including unexpected vendor extensions.
		for kind, data := range expr {
			// Stop at the first ingress restriction or unsupported expression.
			if blocked, err := g.expressionBlocks(chain, kind, data); blocked || err != nil {
				return blocked, err
			}
		}
	}
	return false, nil
}

// expressionBlocks classifies nft verdicts and recursively checks jump targets.
func (g *nftGraph) expressionBlocks(chain nftChain, kind string, data json.RawMessage) (bool, error) {
	// Classify verdicts before following cross-chain targets.
	switch kind {
	// Compatibility extensions require separate trust and shape validation.
	case "xt":
		return g.compatibilityBlocks(data)
	// These verdicts explicitly restrict or divert ingress.
	case "drop", "reject", "queue":
		return true, nil
	// Follow jump targets within the same family and table.
	case "jump", "goto":
		var target struct{ Target string }
		// Reject malformed jump targets before recursion.
		if err := json.Unmarshal(data, &target); err != nil {
			return true, err
		}
		// A verified pinned cluster may own its generated KUBE chains.
		if g.trusted && strings.HasPrefix(target.Target, "KUBE-") {
			return false, nil
		}
		return g.blocks(nftKey(chain.Family, chain.Table, target.Target))
	// These known nonblocking expressions do not establish an ingress restriction.
	case "match", "counter", "accept", "return", "dnat", "snat", "masquerade", "redirect", "notrack", "ct", "meta", "mangle", "limit", "log":
		return false, nil
	// Unknown expression kinds require explicit administrator review.
	default:
		return true, fmt.Errorf("unsupported ingress expression %s", kind)
	}
}

// compatibilityBlocks permits only reviewed non-verdict compatibility extensions.
func (g *nftGraph) compatibilityBlocks(data json.RawMessage) (bool, error) {
	var extension struct{ Type, Name string }
	// Malformed compatibility metadata cannot authorize adoption.
	if err := json.Unmarshal(data, &extension); err != nil {
		return true, err
	}
	// Permit only trusted comment or conntrack match extensions here.
	if !g.trusted || extension.Type != "match" || (extension.Name != "comment" && extension.Name != "conntrack") {
		return true, fmt.Errorf("unsupported ingress compatibility expression")
	}
	return false, nil
}

// compatibilityExpression reports opaque matching and owned verdict evidence separately.
func compatibilityExpression(kind string, data json.RawMessage) (opaque, verdict, valid bool) {
	// Track opaque matching evidence separately from terminal verdict evidence.
	switch kind {
	// Validate the precise xtables extension shape.
	case "xt":
		return compatibilityExtension(data)
	// Ordinary matches and counters provide no ownership verdict themselves.
	case "match", "counter":
		return false, false, true
	// An accept expression supplies terminal verdict evidence.
	case "accept":
		return false, true, true
	// Only generated cluster jump targets supply trusted jump evidence.
	case "jump":
		var target struct{ Target string }
		// Reject malformed or unrelated compatibility jump targets.
		if json.Unmarshal(data, &target) != nil || !strings.HasPrefix(target.Target, "KUBE-") {
			return false, false, false
		}
		return false, true, true
	// Unrecognized expression forms cannot prove an owned compatibility rule.
	default:
		return false, false, false
	}
}

// compatibilityExtension accepts only the exact extension forms covered by textual views.
func compatibilityExtension(data json.RawMessage) (opaque, verdict, valid bool) {
	var extension struct{ Type, Name string }
	// Refuse undecodable compatibility metadata.
	if json.Unmarshal(data, &extension) != nil {
		return false, false, false
	}
	// The pinned textual view may represent REJECT as an opaque target extension.
	if extension.Type == "target" && extension.Name == "REJECT" {
		return true, true, true
	}
	valid = extension.Type == "match" && (extension.Name == "comment" || extension.Name == "conntrack" || extension.Name == "multiport" || extension.Name == "addrtype")
	return true, false, valid
}
