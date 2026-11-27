package cli

import (
	"reflect"
	"testing"
)

// TestActionGrantCLI preserves the selected actions instead of converting them to deploy or all access.
func TestActionGrantCLI(t *testing.T) {
	for _, pairing := range []bool{false, true} {
		fake := &fakeClient{}
		fake.reply = func(c call) (any, error) {
			body := c.body.(map[string]any)
			if pairing {
				body = body["scope"].(map[string]any)
			} else if body["scope"] != "actions" {
				t.Fatal(body)
			}
			if !reflect.DeepEqual(body["actions"], []string{"servers.add"}) || body["durationSeconds"] != int64(3600) || body["maxUses"] != 2 {
				t.Fatal(body)
			}
			if _, ok := body["apps"]; ok {
				t.Fatal("global grant gained app scope", body)
			}
			return map[string]string{"id": "grant"}, nil
		}
		args := []string{"authorize", "--device", "device", "--on", "local", "--scope", "actions"}
		if pairing {
			args = []string{"pairing", "code", "request"}
		}
		args = append(args, "--actions", "servers.add", "--for", "1h", "--max-uses", "2", "--socket", "/tmp/controller.sock")
		if _, err := invoke(args, "", Options{Client: fake, Home: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		if len(fake.calls) != 1 {
			t.Fatal(fake.calls)
		}
	}
}

// TestServerApproveRejectsUnreviewedInput validates arguments before connecting to a controller.
func TestServerApproveRejectsUnreviewedInput(t *testing.T) {
	for _, args := range [][]string{{"server", "approve", "01234567-89ABCDEF"}, {"server", "approve", "01234567-89ABCDEF", "--yes"}} {
		fake := &fakeClient{}
		if _, err := invoke(args, "yes\n", Options{Client: fake, Home: t.TempDir()}); err == nil || len(fake.calls) != 0 {
			t.Fatal("approval bypass", args, err)
		}
	}
}
