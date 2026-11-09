package nginx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/controledge"
	"hostlier/internal/model"
)

// Refreshing the generated controller virtual host preserves application
// configuration byte for byte in both standalone and gateway installations.
func TestUpgradeControllerGRPC(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		var runs int
		o := defaults(Options{Directory: t.TempDir(), Gateway: gateway, Run: func(context.Context, string, ...string) error { runs++; return nil }})
		routes := []Route{{Project: model.Project{Name: "app", Route: "app.example"}, Upstreams: []string{"127.0.0.1:8080"}}}
		data, err := Render(o, "control.example", "127.0.0.1:7463", routes)
		if err != nil {
			t.Fatal(err)
		}
		var old strings.Builder
		for _, line := range strings.SplitAfter(string(data), "\n") {
			if !strings.Contains(line, "location ^~ "+controledge.RPCPrefix) {
				old.WriteString(line)
			}
		}
		path := filepath.Join(o.Directory, "nginx.conf")
		if err := os.WriteFile(path, []byte(old.String()), 0600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := UpgradeController(context.Background(), o, "control.example", "127.0.0.1:7463"); err != nil {
				t.Fatal(err)
			}
		}
		result, err := os.ReadFile(path)
		if err != nil || string(result) != string(data) || runs != 2 {
			t.Fatal("expected one validation/reload and unchanged application config", runs, err)
		}
	}
}
