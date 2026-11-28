package runtime

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"hostlier/internal/model"
)

// ReconcileServiceCrons runs after service readiness and removes obsolete job namespaces.
func (k *K3s) ReconcileServiceCrons(ctx context.Context, service model.Project, dc model.DeployContext) error {
	k.base.mu.Lock()
	defer k.base.mu.Unlock()
	selector := "io.hostlier.owner=" + k.base.owner + ",io.hostlier.app=" + service.Name + "," + serviceCronLabel
	raw, err := k.run(ctx, []string{"get", "namespaces", "-l", selector, "-o", "json"}, nil)
	if err != nil {
		return err
	}
	var list struct {
		Items []map[string]any
	}
	if json.Unmarshal(raw, &list) != nil {
		return model.Err("KUBERNETES_RESPONSE", "Invalid service cron namespace inventory")
	}
	for _, object := range list.Items {
		meta, _ := object["metadata"].(map[string]any)
		labels, _ := meta["labels"].(map[string]any)
		job, _ := labels[serviceCronLabel].(string)
		target := service.Name + "/" + job
		if job == "" || meta["name"] != k.cronNamespace(target) || !k.cronOwned(object, target) {
			return model.Err("OWNERSHIP", "Refusing to reconcile an unowned cron namespace")
		}
		if _, desired := service.Cron[job]; !desired {
			if err := k.deleteCron(ctx, target); err != nil {
				return err
			}
		}
	}
	for _, job := range slices.Sorted(maps.Keys(service.Cron)) {
		if _, err := k.deployCron(ctx, model.ServiceCronProject(service, service.Cron[job]), dc, service.Name+"/"+job); err != nil {
			return err
		}
	}
	return nil
}

func (k *K3s) deleteCron(ctx context.Context, target string) error {
	ns := k.cronNamespace(target)
	object, err := k.cronObject(ctx, "namespace", ns, "", target)
	if err != nil || object == nil {
		return err
	}
	return k.remove(ctx, "namespace", ns, "")
}

// ServiceCronStatus reads the live suspension bit without changing the service status.
func (k *K3s) ServiceCronStatus(ctx context.Context, target string) (string, error) {
	schedule, _, err := k.findCron(ctx, target)
	if err != nil {
		return "", err
	}
	spec, _ := schedule["spec"].(map[string]any)
	if spec["suspend"] == true {
		return "paused", nil
	}
	return "scheduled", nil
}
