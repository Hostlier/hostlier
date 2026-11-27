package controllercmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type testMacLaunchd struct {
	path      string
	changes   []string
	unloading bool
}

func (l *testMacLaunchd) run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "print":
		if args[1] == "gui/501" {
			return "", nil
		}
		if l.path != "" {
			out := "\tpath = " + l.path + "\n"
			if l.unloading {
				l.path, l.unloading = "", false
			}
			return out, nil
		}
		return "Could not find service", errors.New("service missing")
	case "bootstrap":
		if l.path != "" {
			return "service label has not unloaded", errors.New("already registered")
		}
		l.path = args[2]
	case "bootout":
		l.unloading = true
	default:
		return "", errors.New("unexpected launchctl command")
	}
	l.changes = append(l.changes, args[0])
	return "", nil
}

func TestMacInitPreservesReviewedConfigurationAndLaunchAgent(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "hl-mac-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	executable := filepath.Join(home, "source controller")
	if err := os.WriteFile(executable, []byte("verified controller"), 0700); err != nil {
		t.Fatal(err)
	}
	launchd := &testMacLaunchd{}
	var published macInitPlan
	sys := macInitSystem{home: home, uid: 501, executable: executable, launch: launchd.run, ready: func(_ context.Context, p macInitPlan) error {
		published = p
		return nil
	}}
	run := func(args ...string) (macInitPlan, error) {
		t.Helper()
		var out bytes.Buffer
		args = append(args, "--json")
		err := runMacInit(context.Background(), args, strings.NewReader(""), &out, io.Discard, sys)
		var p macInitPlan
		if err == nil {
			decoder := json.NewDecoder(&out)
			if err := decoder.Decode(&p); err != nil {
				t.Fatal(err, out.String())
			}
			if !strings.Contains(strings.Join(args, " "), "--dry-run") {
				var result map[string]bool
				if err := decoder.Decode(&result); err != nil || !result["installed"] {
					t.Fatal(err, out.String())
				}
			}
		}
		return p, err
	}
	args := []string{"--domain", "example.test", "--port", "17463", "--fleet-address", "127.0.0.1:17464"}
	preview, err := run(append(args, "--dry-run")...)
	if err != nil || preview.Runtime != "control-plane" || preview.APIAddress != "http://127.0.0.1:17463" || preview.Fingerprint == "" {
		t.Fatal(preview, err)
	}
	if _, err := os.Stat(preview.StateDir); !os.IsNotExist(err) || len(launchd.changes) != 0 {
		t.Fatal("preview changed the Mac", err, launchd.changes)
	}
	_, err = run(append(args, "--yes", "--approved-fingerprint", "stale")...)
	assertInitError(t, err, "STALE_PLAN")
	if len(launchd.changes) != 0 {
		t.Fatal("stale approval changed launchd", launchd.changes)
	}
	if _, err := run(append(args, "--yes", "--approved-fingerprint", preview.Fingerprint)...); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(preview.LaunchAgent)
	if err != nil || !bytes.Contains(plist, []byte("<key>KeepAlive</key><true/>")) || !bytes.Contains(plist, []byte("<key>RunAtLoad</key><true/>")) {
		t.Fatal("missing automatic launchd startup", err)
	}
	binary, err := os.ReadFile(preview.Executable)
	if err != nil || string(binary) != "verified controller" {
		t.Fatal("controller binary mismatch", err)
	}
	if _, err := run("--yes"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(launchd.changes, []string{"bootstrap"}) || published.APIAddress != preview.APIAddress {
		t.Fatalf("idempotent init lost settings or restarted: %+v %v", published, launchd.changes)
	}
	sys.ready = func(context.Context, macInitPlan) error { return errors.New("candidate failed") }
	if _, err := run("--yes", "--port", "17480"); err == nil || !strings.Contains(err.Error(), "candidate failed") {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(preview.LaunchAgent)
	want := []string{"bootstrap", "bootout", "bootstrap", "bootout", "bootstrap"}
	if err != nil || !bytes.Equal(restored, plist) || !reflect.DeepEqual(launchd.changes, want) {
		t.Fatalf("previous agent was not restored: %v %v", err, launchd.changes)
	}
	if err := os.WriteFile(preview.LaunchAgent, []byte("<plist/>"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = run("--yes")
	assertInitError(t, err, "INSTALL_OWNERSHIP")
}

func TestControlPlaneRecoversOwnedStaleAdminSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hl-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "admin.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	assertInitError(t, checkControlPlaneSocket(path, true), "INSTALL_STATE")
	listener.Close()
	if err := checkControlPlaneSocket(path, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("stale socket was retained", err)
	}
	if err := os.WriteFile(path, []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	assertInitError(t, checkControlPlaneSocket(path, true), "INSTALL_PATH")
}
