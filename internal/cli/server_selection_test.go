package cli

import (
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
