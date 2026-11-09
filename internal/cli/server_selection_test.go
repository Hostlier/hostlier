package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"hostlier/internal/client"
	"hostlier/internal/fleet"
)

func TestSelectControllerForSubsequentCommands(t *testing.T) {
	home := t.TempDir()
	store := client.NewStore(home)
	for _, name := range []string{"other", "prod"} {
		if err := store.Save(name, "https://"+name+".example.com", name, name); err != nil {
			t.Fatal(err)
		}
	}
	remote := &fakeClient{reply: func(c call) (any, error) {
		if c.path == "/v1/hosts" {
			return map[string]any{"hosts": map[string]any{"box": fleet.HostRecord{Name: "box"}}}, nil
		}
		return map[string]string{}, nil
	}}
	opts := Options{Home: home, Client: remote}
	run := func(args ...string) string {
		t.Helper()
		out, err := invoke(args, "", opts)
		if err != nil {
			t.Fatal(args, err)
		}
		return out
	}
	run("server", "use", "box")
	if out := run("server", "list"); !strings.Contains(out, `"currentHost":"box"`) {
		t.Fatal(out)
	}
	for _, test := range []struct {
		args []string
		path string
	}{
		{[]string{"status"}, "/v1/hosts/box/proxy/status"},
		{[]string{"telemetry", "logs", "--app", "demo"}, "/v1/hosts/box/proxy/telemetry/history?app=demo&signal=logs"},
		{[]string{"status", "--server", "other"}, "/v1/status"},
		{[]string{"status", "--on", "local"}, "/v1/status"},
		{[]string{"status", "--socket", "/local"}, "/v1/status"},
	} {
		run(test.args...)
		if got := remote.calls[len(remote.calls)-1].path; got != test.path {
			t.Fatal(test.args, got)
		}
	}
	if _, err := invoke([]string{"server", "use", "other/missing"}, "", opts); err == nil {
		t.Fatal("unknown box selected")
	}
	p, _ := store.List()
	if p.Current != "prod" || p.CurrentHost != "box" {
		t.Fatal(p)
	}
	run("server", "use", "other/box")
	p, _ = store.List()
	if p.Current != "other" || p.CurrentHost != "box" {
		t.Fatal(p)
	}
	run("server", "use", "prod")
	run("status")
	if got := remote.calls[len(remote.calls)-1].path; got != "/v1/status" {
		t.Fatal(got)
	}
}

type memberRequester struct {
	member  fleet.Member
	changes []fleet.MasterChange
}

func (m *memberRequester) Request(_ context.Context, method, path string, body, out any) error {
	if path != "/v1/fleet/master" {
		return fmt.Errorf("unexpected path %s", path)
	}
	if method == "GET" {
		*out.(*fleet.Member) = m.member
		return nil
	}
	c := body.(fleet.MasterChange)
	if c.Previous != m.member.Master {
		return fmt.Errorf("stale pin")
	}
	m.changes = append(m.changes, c)
	m.member.Master = c.Fingerprint
	return nil
}

func TestJoinInstalledBox(t *testing.T) {
	before, after, boxPin := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	for _, scenario := range []string{"success", "reenrolled", "denied", "unreachable", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			member := &memberRequester{member: fleet.Member{Master: before, Hostname: "hostlier.box.example.com", Fingerprint: boxPin}}
			accepted := false
			requester := &fakeClient{reply: func(c call) (any, error) {
				switch c.path {
				case "/v1/hosts/identity":
					return map[string]string{"fingerprint": after}, nil
				case "/v1/hosts":
					if c.method == "GET" {
						hosts := map[string]fleet.HostRecord{}
						if scenario == "reenrolled" {
							hosts["blue"] = fleet.HostRecord{Name: "blue", URL: "https://private.box.example.com:7464", Fingerprint: boxPin}
						}
						if scenario == "conflict" {
							hosts["box"] = fleet.HostRecord{Fingerprint: "someone-else"}
						}
						return map[string]any{"hosts": hosts}, nil
					}
					if scenario == "reenrolled" {
						t.Fatal("requested duplicate enrollment")
					}
					if scenario == "denied" {
						return nil, fmt.Errorf("denied")
					}
					if member.member.Master != before {
						t.Fatal("switched before approval")
					}
					expected := fleet.HostRecord{Name: "box", URL: "https://hostlier.box.example.com:7464", Fingerprint: boxPin}
					if c.body != expected {
						t.Fatalf("enrollment %#v", c.body)
					}
					accepted = true
					return expected, nil
				case "/v1/hosts/blue/proxy/host-state":
					if scenario != "reenrolled" || member.member.Master != after {
						t.Fatal("unexpected alias")
					}
					return map[string]int{"generation": 42}, nil
				case "/v1/hosts/box/proxy/host-state":
					if !accepted || member.member.Master != after {
						t.Fatal("probe out of order")
					}
					if scenario == "unreachable" {
						return nil, fmt.Errorf("unreachable")
					}
					return map[string]int{"generation": 42}, nil
				}
				return nil, fmt.Errorf("unexpected %s", c.path)
			}}
			s := newSession(context.Background(), arguments{args: []string{"server", "join", "prod"}, flags: map[string]string{}}, strings.NewReader(""), io.Discard, io.Discard, Options{Home: t.TempDir(), Client: requester})
			err := s.joinInstalledBox(member)
			if scenario == "success" || scenario == "reenrolled" {
				if err != nil || member.member.Master != after {
					t.Fatal(err, member)
				}
				return
			}
			if err == nil || member.member.Master != before {
				t.Fatal(err, member)
			}
			if scenario == "unreachable" && len(member.changes) != 2 {
				t.Fatal(member)
			}
		})
	}
}
