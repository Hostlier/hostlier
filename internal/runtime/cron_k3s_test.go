package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"hostlier/internal/model"
)

func TestCronK3sManifests(t *testing.T) {
	adapter, err := New(Options{Type: "k3s", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	k := adapter.(*K3s)
	spec := model.Project{
		Name: "report", Kind: model.CronKind, Schedule: "0 7 * * 1-5", TimeZone: "Europe/London",
		Image: "example/report@sha256:" + strings.Repeat("a", 64), Command: []string{"report"},
		Secrets:   map[string]string{"TOKEN": "fixture"},
		Resources: &model.Limits{CPU: "0.5", Memory: "128Mi"},
		Requests:  &model.Limits{CPU: "0.25", Memory: "64Mi"},
	}
	ctx := model.DeployContext{OperationID: "approved-cron", Secrets: map[string]string{"TOKEN": "sensitive"}}
	manifests, err := k.cronManifests(spec, ctx, nil, true, spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"Namespace", "NetworkPolicy", "Secret", "CronJob"}
	if len(manifests) != len(order) {
		t.Fatalf("unexpected objects: %v", manifests)
	}
	for i, kind := range order {
		if manifests[i]["kind"] != kind {
			t.Fatalf("object %d should be %s, got %v", i, kind, manifests[i]["kind"])
		}
	}
	if k.cronNamespace(spec.Name) == k.namespace(spec.Name) {
		t.Fatal("cron namespace can collide with an ordinary app")
	}
	data, err := json.Marshal(manifests[len(manifests)-1])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"timeZone":"Europe/London"`, `"suspend":true`, `"concurrencyPolicy":"Forbid"`, `"ttlSecondsAfterFinished":86400`,
		`"successfulJobsHistoryLimit":3`, `"failedJobsHistoryLimit":3`, `"backoffLimit":0`, `"restartPolicy":"Never"`,
		`"automountServiceAccountToken":false`, `"requests":{"cpu":"0.25","memory":"64Mi"}`, `"secretKeyRef"`, `"io.hostlier.revision":"approved-cron"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in %s", want, data)
		}
	}
	for _, unexpected := range []string{`"ports"`, `"readinessProbe"`, `"hostNetwork"`, "sensitive"} {
		if strings.Contains(string(data), unexpected) {
			t.Errorf("cron template contains %s", unexpected)
		}
	}
}
