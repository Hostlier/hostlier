package installer

import (
	"encoding/json"
	"net"
	"reflect"
	"regexp"
)

// ownedDockerRule recognizes pinned Docker rules whose scope cannot block public web ingress.
func (g *nftGraph) ownedDockerRule(chain nftChain, rule nftRule) bool {
	// Require the exact validated scope before accepting this branch.
	if !g.docker || (chain.Family != "ip" && chain.Family != "ip6") {
		return false
	}
	expressions := withoutCounters(rule.Expr)
	// The NAT entry jump and destination translation are exact Docker forms, not arbitrary chains.
	if chain.Table == "nat" && chain.Name == "DOCKER" {
		return dockerNATPublication(expressions)
	}
	// No other Docker-named chain may hide ingress restrictions.
	if chain.Family != "ip" || chain.Table != "raw" || chain.Name != "PREROUTING" {
		return false
	}
	// Raw anti-spoof rules must be backed by an independently visible Docker publication.
	return g.dockerRawPublication(expressions)
}

// withoutCounters removes only accounting expressions before strict shape comparison.
func withoutCounters(expressions []map[string]json.RawMessage) []map[string]json.RawMessage {
	result := []map[string]json.RawMessage{}
	// Inspect each expression or publication before trusting the scoped rule.
	for _, expression := range expressions {
		// Require the exact validated scope before accepting this branch.
		if _, ok := expression["counter"]; ok && len(expression) == 1 {
			continue
		}
		result = append(result, expression)
	}
	return result
}

// exactExpression compares decoded objects without relying on JSON field order or whitespace.
func exactExpression(expression map[string]json.RawMessage, expected string) bool {
	actual, _ := json.Marshal(expression)
	var a, b any
	// Require the exact validated scope before accepting this branch.
	if json.Unmarshal(actual, &a) != nil || json.Unmarshal([]byte(expected), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// dockerNATEntry accepts only the local-destination lookup followed by the exact Docker jump.
func dockerNATEntry(expressions []map[string]json.RawMessage) bool {
	// Require the exact validated scope before accepting this branch.
	if len(expressions) != 2 || !exactExpression(expressions[1], `{"jump":{"target":"DOCKER"}}`) {
		return false
	}
	return exactExpression(expressions[0], `{"xt":{"type":"match","name":"addrtype"}}`) || exactExpression(expressions[0], `{"match":{"op":"==","left":{"fib":{"result":"type","flags":["daddr"]}},"right":"local"}}`)
}

// dockerMatch extracts one exact equality or inequality against a known packet field.
func dockerMatch(expression map[string]json.RawMessage, left, operator string) (any, bool) {
	var match struct {
		Op    string
		Left  json.RawMessage
		Right any
	}
	// Require the exact validated scope before accepting this branch.
	if len(expression) != 1 || json.Unmarshal(expression["match"], &match) != nil || match.Op != operator {
		return nil, false
	}
	var fields map[string]json.RawMessage
	// Unknown match properties must not be silently ignored by the typed decoder.
	if json.Unmarshal(expression["match"], &fields) != nil || len(fields) != 3 {
		return nil, false
	}
	var actual, expected any
	// Parsed field equality prevents hidden masks or extra selectors broadening the rule.
	if json.Unmarshal(match.Left, &actual) != nil || json.Unmarshal([]byte(left), &expected) != nil {
		return nil, false
	}
	return match.Right, reflect.DeepEqual(actual, expected)
}

// dockerScope validates IPv4 private destinations and exact generated bridge names.
func dockerScope(expressions []map[string]json.RawMessage) (string, string, bool) {
	// Require the exact validated scope before accepting this branch.
	if len(expressions) < 2 {
		return "", "", false
	}
	destination, ok := dockerMatch(expressions[0], `{"payload":{"protocol":"ip","field":"daddr"}}`, "==")
	address, text := destination.(string)
	ip := net.ParseIP(address)
	// A private container address cannot stand for the host's public web listener.
	if !ok || !text || ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return "", "", false
	}
	incoming, ok := dockerMatch(expressions[1], `{"meta":{"key":"iifname"}}`, "!=")
	bridge, text := incoming.(string)
	return address, bridge, ok && text && (bridge == "lo" || regexp.MustCompile(`^br-[a-f0-9]{12}$`).MatchString(bridge))
}

// dockerPort requires the dynamic TCP publication rather than public HTTP or HTTPS.
func dockerPort(expression map[string]json.RawMessage) bool {
	value, ok := dockerMatch(expression, `{"payload":{"protocol":"tcp","field":"dport"}}`, "==")
	port, number := value.(float64)
	return ok && number && port >= 1024 && port <= 65535 && port == float64(int(port))
}

// dockerNATPublication accepts loopback-only high-port publication with a Docker DNAT target.
func dockerNATPublication(expressions []map[string]json.RawMessage) bool {
	address, bridge, ok := dockerScope(expressions)
	// Require the exact validated scope before accepting this branch.
	if !ok || address != "127.0.0.1" || bridge == "lo" || len(expressions) != 4 || !dockerPort(expressions[2]) {
		return false
	}
	return exactExpression(expressions[3], `{"xt":{"type":"target","name":"DNAT"}}`) || hasSingleExpression(expressions[3], "dnat")
}

// hasSingleExpression requires a terminal verdict to be the only expression member.
func hasSingleExpression(expression map[string]json.RawMessage, key string) bool {
	_, ok := expression[key]
	return ok && len(expression) == 1
}

// dockerRawPublication cross-checks each anti-spoof rule with its visible publication scope.
func (g *nftGraph) dockerRawPublication(expressions []map[string]json.RawMessage) bool {
	address, bridge, ok := dockerScope(expressions)
	// Require the exact validated scope before accepting this branch.
	if !ok || !exactExpression(expressions[len(expressions)-1], `{"drop":null}`) {
		return false
	}
	// Loopback protection must match the exact high port in a Docker NAT publication.
	if dockerRawLoopbackScope(address, bridge, expressions) {
		return g.dockerLoopbackPublication(expressions)
	}
	// Container isolation must match a filter accept scoped to the same bridge and destination.
	if len(expressions) != 3 || bridge == "lo" || net.ParseIP(address).IsLoopback() {
		return false
	}
	// Require a separate published endpoint with the same address and interface.
	for _, rule := range g.rules[nftKey("ip", "filter", "DOCKER")] {
		candidate := withoutCounters(rule.Expr)
		// Require the exact validated scope before accepting this branch.
		if dockerFilterPublication(candidate, expressions) {
			return true
		}
	}
	return false
}

// dockerLoopbackPublication compares destination and port with the owned NAT publication.
func (g *nftGraph) dockerLoopbackPublication(expressions []map[string]json.RawMessage) bool {
	// Inspect each expression or publication before trusting the scoped rule.
	for _, rule := range g.rules[nftKey("ip", "nat", "DOCKER")] {
		candidate := withoutCounters(rule.Expr)
		// Require the exact validated scope before accepting this branch.
		if dockerNATPublication(candidate) && reflect.DeepEqual(candidate[0], expressions[0]) && reflect.DeepEqual(candidate[2], expressions[2]) {
			return true
		}
	}
	return false
}

// dockerFilterPublication requires matching destination, ingress, egress and a TCP accept.
func dockerFilterPublication(candidate, raw []map[string]json.RawMessage) bool {
	// Require the exact validated scope before accepting this branch.
	if len(candidate) != 5 || !reflect.DeepEqual(candidate[0], raw[0]) || !reflect.DeepEqual(candidate[1], raw[1]) {
		return false
	}
	_, bridge, ok := dockerScope(raw)
	outgoing, valid := dockerMatch(candidate[2], `{"meta":{"key":"oifname"}}`, "==")
	// Only an exact bridge destination plus TCP port demonstrates a container publication.
	port, tcp := dockerMatch(candidate[3], `{"payload":{"protocol":"tcp","field":"dport"}}`, "==")
	number, numeric := port.(float64)
	return ok && valid && outgoing == bridge && tcp && validDockerPort(number, numeric) && exactExpression(candidate[4], `{"accept":null}`)
}

// dockerRawLoopbackScope confines anti-spoof recognition to a loopback high-port publication.
func dockerRawLoopbackScope(address, bridge string, expressions []map[string]json.RawMessage) bool {
	return address == "127.0.0.1" && bridge == "lo" && len(expressions) == 4 && dockerPort(expressions[2])
}

// validDockerPort validates a concrete TCP endpoint port without adopting a wildcard.
func validDockerPort(port float64, numeric bool) bool {
	return numeric && port > 0 && port <= 65535 && port == float64(int(port))
}
