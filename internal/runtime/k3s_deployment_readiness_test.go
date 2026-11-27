package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hostlier/internal/model"
)

// Kubernetes rollout success alone must not complete a deployment, replacement,
// or deployment of a retained rollback specification.
func TestK3sDeployAndRollbackWaitForPublicReadiness(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(map[int]string{1: "legacy", 2: "workload"}[version], func(t *testing.T) {
			var ready atomic.Bool
			probed := make(chan struct{}, 1)
			k := lifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ready" {
					t.Errorf("unexpected readiness path %s", r.URL.Path)
				}
				if !ready.Load() {
					w.WriteHeader(http.StatusBadGateway)
					select {
					case probed <- struct{}{}:
					default:
					}
				}
			})
			k.options.KubeRun = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), "get service app") {
					return json.Marshal(map[string]any{"metadata": k.metadata("app", "app"), "spec": map[string]any{"clusterIP": "10.43.0.10"}})
				}
				for i, arg := range args {
					if arg == "get" && i+2 < len(args) && args[i+1] == "deployment" {
						// Retained Deployment objects remain owned during failed-attempt recovery.
						return json.Marshal(map[string]any{"metadata": k.metadata("app", args[i+2])})
					}
				}
				return nil, nil
			}
			if err := k.base.save(state{Apps: map[string]appRecord{}}); err != nil {
				t.Fatal(err)
			}
			original := model.Project{Version: version, Name: "app", Image: "example/image@sha256:" + strings.Repeat("a", 64), Port: 8080, Route: "https://app.example.com", Health: &model.Health{Path: "/ready"}}
			if version == 2 {
				original.Endpoint = &model.Endpoint{Exposure: "public", Port: 8080, Route: original.Route}
			}
			replacement := original
			replacement.Image = "example/image@sha256:" + strings.Repeat("b", 64)
			previous := ""
			for _, step := range []struct {
				name string
				spec model.Project
				fail bool
			}{{"deploy", original, false}, {"replace", replacement, false}, {"rollback", original, false}, {"deploy-unready", replacement, true}, {"rollback-unready", original, true}} {
				ready.Store(false)
				// Discard any earlier failed probe notification.
				select {
				case <-probed:
				default:
				}
				ctx, cancel := context.WithCancel(context.Background())
				result := make(chan error, 1)
				go func() {
					_, err := k.Deploy(ctx, step.spec, model.DeployContext{OperationID: step.name})
					result <- err
				}()
				select {
				case err := <-result:
					cancel()
					t.Fatalf("%s returned before probing public readiness: %v", step.name, err)
				case <-probed:
				case <-time.After(30 * time.Second):
					cancel()
					t.Fatal("public readiness was never probed")
				}
				s, err := k.base.read()
				if err != nil || s.Apps["app"].RuntimeID != previous {
					cancel()
					t.Fatalf("%s committed before readiness: %+v, %v", step.name, s, err)
				}
				select {
				case err := <-result:
					cancel()
					t.Fatalf("%s completed while public health returned 502: %v", step.name, err)
				default:
				}
				if step.fail {
					cancel()
				} else {
					ready.Store(true)
				}
				select {
				case err = <-result:
				case <-time.After(30 * time.Second):
					cancel()
					t.Fatal("deployment did not finish")
				}
				cancel()
				if (err != nil) != step.fail {
					t.Fatalf("%s result: %v", step.name, err)
				}
				if !step.fail {
					previous = "app-" + shortHash(step.name)
				}
				s, err = k.base.read()
				if err != nil || s.Apps["app"].RuntimeID != previous {
					t.Fatalf("%s persisted wrong revision: %+v, %v", step.name, s, err)
				}
			}
		})
	}
}
