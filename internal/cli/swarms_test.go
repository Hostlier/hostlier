package cli

import (
	"reflect"
	"testing"

	"hostlier/internal/model"
)

// TestSwarmCLI uses the normal parser, profile routing and retained approval transport.
func TestSwarmCLI(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, tc := range []struct {
			args  []string
			path  string
			reads int
		}{
			{[]string{"swarm", "list"}, "/v1/swarms", 0},
			{[]string{"swarm", "show", "research"}, "/v1/swarms/research", 0},
			{[]string{"swarm", "members", "research"}, "/v1/swarms/research/apps?includeDeleted=true", 0},
			{[]string{"swarm", "create", "research"}, "/v1/swarms", 0},
			{[]string{"swarm", "delete", "research"}, "/v1/swarms/sw_research", 1},
			{[]string{"swarm", "move", "api", "--from", "default", "--to", "research"}, "/v1/swarms/sw_research/members/api", 2},
			{[]string{"app", "list", "--swarm", "research"}, "/v1/swarms/research/apps", 0},
			{[]string{"app", "list", "--swarm", "research", "--all"}, "/v1/swarms/research/apps?includeDeleted=true", 0},
		} {
			fake := &fakeClient{}
			prefix := ""
			if remote {
				prefix = "/v1/hosts/worker/proxy"
			}
			fake.reply = func(c call) (any, error) {
				if c.method == "GET" && c.path == prefix+"/swarms/default" {
					return model.Swarm{ID: "default", Name: "default"}, nil
				}
				if c.method == "GET" && (c.path == "/v1/swarms/default") {
					return model.Swarm{ID: "default", Name: "default"}, nil
				}
				return model.Swarm{ID: "sw_research", Name: "research"}, nil
			}
			args := append([]string{}, tc.args...)
			args = append(args, "--server", "master")
			expected := tc.path
			if remote {
				args = append(args, "--on", "worker")
				expected = prefix + tc.path[3:]
			}
			if _, err := invoke(args, "yes\n", Options{Client: fake, Home: t.TempDir()}); err != nil {
				t.Fatal(args, err)
			}
			if len(fake.calls) != tc.reads+1 || fake.calls[len(fake.calls)-1].path != expected {
				t.Fatal(args, fake.calls)
			}
			if tc.reads == 2 {
				body := fake.calls[2].body
				if !reflect.DeepEqual(body, map[string]string{"from": "default"}) && !reflect.DeepEqual(body, map[string]any{"from": "default"}) {
					t.Fatal("move did not pin source identity", body)
				}
			}
		}
	}
}

// TestSwarmCLIRejectsIgnoredScope prevents silent scope loss or unintended authority broadening.
func TestSwarmCLIRejectsIgnoredScope(t *testing.T) {
	for _, args := range [][]string{
		{"swarm", "create", "INVALID"}, {"swarm", "move", "api", "--to", "research"},
		{"swarm", "list", "--from", "default"}, {"swarm", "list", "extra"},
		{"app", "stop", "api", "--swarm", "research"}, {"pairing", "list", "--swarm", "research"},
	} {
		fake := &fakeClient{}
		if _, err := invoke(append(args, "--server", "master"), "yes\n", Options{Client: fake, Home: t.TempDir()}); err == nil || len(fake.calls) != 0 {
			t.Fatal(args, err, fake.calls)
		}
	}
	s := &session{flags: map[string]string{"swarm": "research"}}
	if _, err := s.selectProjectSwarm(model.Project{Swarm: "private"}); err == nil {
		t.Fatal("conflicting project scope accepted")
	}
}

// TestSwarmGrantCLI verifies both signed delegation and local pairing carry the explicit whole-group selector.
func TestSwarmGrantCLI(t *testing.T) {
	for _, pairing := range []bool{false, true} {
		fake := &fakeClient{reply: func(c call) (any, error) {
			body := c.body.(map[string]any)
			if pairing {
				body = body["scope"].(map[string]any)
			}
			if body["swarm"] != "research" || !reflect.DeepEqual(body["actions"], []string{"apps.deploy", "swarms.view"}) {
				t.Fatal(body)
			}
			if _, ok := body["apps"]; ok {
				t.Fatal("whole-group grant gained app restriction")
			}
			return map[string]string{"id": "grant"}, nil
		}}
		args := []string{"access", "authorize", "--device", "operator", "--on", "local", "--scope", "actions"}
		if pairing {
			args = []string{"pairing", "code", "request"}
		}
		args = append(args, "--swarm", "research", "--actions", "apps.deploy,swarms.view", "--for", "1h", "--max-uses", "5", "--socket", "/tmp/controller.sock")
		if _, err := invoke(args, "yes\n", Options{Client: fake, Home: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		if len(fake.calls) != 1 {
			t.Fatal(fake.calls)
		}
	}
}
