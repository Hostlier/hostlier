package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

const cronScheduleName = "schedule"
const cronLabel = "io.hostlier.kind"
const serviceCronLabel = "io.hostlier.cron-job"

var cronRunName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

func (k *K3s) cronNamespace(name string) string {
	if strings.Contains(name, "/") {
		return "hostlier-cronjob-" + k.base.owner + "-" + shortHash(name)
	}
	namespace := "hostlier-cron-" + k.base.owner + "-" + name
	if len(namespace) > 63 {
		return namespace[:46] + "-" + shortHash(namespace)
	}
	return namespace
}

func (k *K3s) cronLabels(name string) map[string]string {
	app, job, attached := strings.Cut(name, "/")
	labels := k.base.labels(app)
	labels[cronLabel] = model.CronKind
	if attached {
		labels[serviceCronLabel] = job
	}
	return labels
}

func (k *K3s) cronMetadata(name, object string) map[string]any {
	return map[string]any{"name": object, "namespace": k.cronNamespace(name), "labels": k.cronLabels(name)}
}

func (k *K3s) cronOwned(object map[string]any, name string) bool {
	metadata, _ := object["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	app, job, attached := strings.Cut(name, "/")
	if !attached {
		return k.owned(object) && labels[cronLabel] == model.CronKind && labels["io.hostlier.app"] == app && labels[serviceCronLabel] == nil
	}
	return k.owned(object) && labels[cronLabel] == model.CronKind && labels["io.hostlier.app"] == app && labels[serviceCronLabel] == job
}

func (k *K3s) cronObject(ctx context.Context, kind, object, namespace, name string) (map[string]any, error) {
	found, err := k.get(ctx, kind, object, namespace)
	if err == nil && found != nil && !k.cronOwned(found, name) {
		return nil, model.Err("OWNERSHIP", "Refusing to use an unowned cron resource")
	}
	return found, err
}

// DeployCron activates an approved immutable schedule after all of its inputs exist.
func (k *K3s) DeployCron(ctx context.Context, p model.Project, dc model.DeployContext) (model.Deployment, error) {
	k.base.mu.Lock()
	defer k.base.mu.Unlock()
	return k.deployCron(ctx, p, dc, p.Name)
}

func (k *K3s) deployCron(ctx context.Context, p model.Project, dc model.DeployContext, target string) (model.Deployment, error) {
	var out model.Deployment
	p, err := config.ValidateProject(p)
	if err != nil {
		return out, err
	}
	if p.Kind != model.CronKind {
		return out, model.Err("INVALID_CONFIG", "Expected a cron project")
	}
	if !digestPattern.MatchString(p.Image) || dc.OperationID == "" {
		return out, model.Err("UNPINNED_IMAGE", "Cron deployment requires an approved immutable image and operation ID")
	}
	if err := k.validateK3sSecurity(p); err != nil {
		return out, err
	}
	if p.Security != nil {
		if err := k.verifyK3sSecurity(ctx); err != nil {
			return out, err
		}
	}
	if err := validateGroupConfigValues(p, dc.Secrets); err != nil {
		return out, err
	}
	pullConfig, err := k.k3sPullConfig(ctx, p)
	if err != nil {
		return out, err
	}
	ns := k.cronNamespace(target)
	if _, err := k.cronObject(ctx, "namespace", ns, "", target); err != nil {
		return out, err
	}
	current, err := k.cronObject(ctx, "cronjob", cronScheduleName, ns, target)
	if err != nil {
		return out, err
	}
	currentSpec, _ := current["spec"].(map[string]any)
	suspended, _ := currentSpec["suspend"].(bool)
	if err := k.importGroupArtifacts(ctx, p); err != nil {
		return out, err
	}
	if err := k.prepareK3sSeccomp(p); err != nil {
		return out, err
	}
	objects, err := k.cronManifests(p, dc, pullConfig, suspended, target)
	if err != nil {
		return out, err
	}
	for _, object := range objects {
		if err := k.apply(ctx, object); err != nil {
			return out, err
		}
	}
	out = model.Deployment{Name: p.Name, RuntimeID: ns + "/" + cronScheduleName, ImageDigest: p.Image, Status: "scheduled"}
	if suspended {
		out.Status = "paused"
	}
	return out, nil
}

func (k *K3s) cronManifests(p model.Project, dc model.DeployContext, pullConfig []byte, suspended bool, target string) ([]map[string]any, error) {
	ns := k.cronNamespace(target)
	namespace := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns, "labels": k.cronLabels(target)}}
	policy, err := k.isolationPolicy(p, dc.DependencyNetworks)
	if err != nil {
		return nil, err
	}
	policy["metadata"] = k.cronMetadata(target, "cron-isolation")
	objects := []map[string]any{namespace, policy}
	revision := "input-" + shortHash(dc.OperationID)
	container := map[string]any{"name": "app", "image": p.Image}
	useImportedK3sImage(container, p.Image)
	envSecrets := map[string]string{}
	for key := range p.Secrets {
		value, ok := dc.Secrets[key]
		if !ok {
			return nil, model.Err("SECRET_NOT_FOUND", "Cron environment secret is unavailable")
		}
		envSecrets[key] = value
	}
	if strings.Contains(target, "/") {
		for key, value := range dc.Secrets {
			if !strings.Contains(key, "/") {
				envSecrets[key] = value
			}
		}
	}
	configureGroupContainer(container, p, "", revision, envSecrets)
	delete(container, "readinessProbe")
	if p.Requests != nil {
		resources, _ := container["resources"].(map[string]any)
		if resources == nil {
			resources = map[string]any{}
			container["resources"] = resources
		}
		resources["requests"] = p.Requests
	}
	if len(envSecrets) != 0 {
		encoded := make(map[string]string, len(envSecrets))
		for key, value := range envSecrets {
			encoded[key] = base64.StdEncoding.EncodeToString([]byte(value))
		}
		objects = append(objects, map[string]any{"apiVersion": "v1", "kind": "Secret",
			"metadata": k.cronMetadata(target, revision), "type": "Opaque", "immutable": true, "data": encoded})
	}
	pod := map[string]any{
		"containers": []any{container}, "restartPolicy": "Never", "automountServiceAccountToken": false,
		"terminationGracePeriodSeconds": model.GraceSeconds(p),
		"nodeSelector":                  map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": goruntime.GOARCH},
	}
	if pullConfig != nil {
		pull := "registry-" + shortHash(dc.OperationID)
		objects = append(objects, map[string]any{"apiVersion": "v1", "kind": "Secret",
			"metadata": k.cronMetadata(target, pull), "type": "kubernetes.io/dockerconfigjson",
			"data": map[string]string{".dockerconfigjson": base64.StdEncoding.EncodeToString(pullConfig)}})
		pod["imagePullSecrets"] = []any{map[string]string{"name": pull}}
	}
	for _, object := range k.componentConfigFiles(p.Name, "app", p, revision, dc.Secrets, container, pod) {
		meta := object["metadata"].(map[string]any)
		object["metadata"] = k.cronMetadata(target, meta["name"].(string))
		objects = append(objects, object)
	}
	var setup []any
	k.secureK3sContainer(p, "primary", container, pod, &setup)
	if len(setup) != 0 {
		pod["initContainers"] = setup
	}
	labels := k.cronLabels(target)
	template := map[string]any{
		"metadata": map[string]any{"labels": labels},
		"spec": map[string]any{"backoffLimit": 0, "ttlSecondsAfterFinished": 86400,
			"template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": pod}},
	}
	meta := k.cronMetadata(target, cronScheduleName)
	meta["annotations"] = map[string]string{"io.hostlier.revision": dc.OperationID}
	schedule := map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob", "metadata": meta,
		"spec": map[string]any{"schedule": p.Schedule, "timeZone": p.TimeZone, "suspend": suspended,
			"concurrencyPolicy": "Forbid", "startingDeadlineSeconds": 300, "successfulJobsHistoryLimit": 3,
			"failedJobsHistoryLimit": 3, "jobTemplate": template},
	}
	return append(objects, schedule), nil
}

func (k *K3s) findCron(ctx context.Context, name string) (map[string]any, string, error) {
	object, err := k.cronObject(ctx, "cronjob", cronScheduleName, k.cronNamespace(name), name)
	if err != nil {
		return nil, "", err
	}
	meta, _ := object["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	if uid == "" {
		return nil, "", model.Err("NOT_FOUND", "Cron schedule not found")
	}
	return object, uid, nil
}

func (k *K3s) CronAction(ctx context.Context, name, action, operation, revision string) (string, error) {
	k.base.mu.Lock()
	defer k.base.mu.Unlock()
	ns := k.cronNamespace(name)
	if action == "delete" {
		return "", k.deleteCron(ctx, name)
	}
	schedule, uid, err := k.findCron(ctx, name)
	if err != nil {
		return "", err
	}
	meta, _ := schedule["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	if (action == "run" || action == "resume") && (revision == "" || annotations["io.hostlier.revision"] != revision) {
		return "", model.Err("STALE_PLAN", "Live cron schedule does not match the approved revision")
	}
	switch action {
	case "pause", "resume":
		data, _ := json.Marshal([]map[string]any{
			{"op": "test", "path": "/metadata/uid", "value": uid},
			{"op": "add", "path": "/spec/suspend", "value": action == "pause"},
		})
		_, err := k.run(ctx, []string{"patch", "cronjob", cronScheduleName, "-n", ns, "--type=json", "--field-manager=hostlier", "-p", string(data)}, nil)
		return "", err
	case "run":
		job := "manual-" + shortHash(operation)
		return job, k.createCronRun(ctx, schedule, uid, name, job)
	default:
		return "", model.Err("INVALID_ACTION", "Invalid cron action")
	}
}

// createCronRun uses a stable name for the accepted operation and the live approved template.
func (k *K3s) createCronRun(ctx context.Context, schedule map[string]any, uid, name, job string) error {
	ns := k.cronNamespace(name)
	spec, _ := schedule["spec"].(map[string]any)
	template, _ := spec["jobTemplate"].(map[string]any)
	jobSpec, ok := template["spec"].(map[string]any)
	if !ok {
		return model.Err("KUBERNETES_RESPONSE", "Cron schedule has no job template")
	}
	existing, err := k.cronObject(ctx, "job", job, ns, name)
	if err != nil {
		return err
	}
	if existing != nil {
		if cronJobOwnedBy(existing, uid) {
			return nil
		}
		return model.Err("OWNERSHIP", "Run belongs to a different cron schedule")
	}
	meta := k.cronMetadata(name, job)
	meta["annotations"] = map[string]string{"io.hostlier.trigger": "manual"}
	meta["ownerReferences"] = []any{map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob", "name": cronScheduleName, "uid": uid, "controller": true,
	}}
	data, _ := json.Marshal(map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": meta, "spec": jobSpec})
	_, err = k.run(ctx, []string{"create", "-f", "-"}, data)
	return err
}

func cronJobOwnedBy(job map[string]any, uid string) bool {
	meta, _ := job["metadata"].(map[string]any)
	owners, _ := meta["ownerReferences"].([]any)
	for _, raw := range owners {
		ref, _ := raw.(map[string]any)
		if ref["kind"] == "CronJob" && ref["uid"] == uid && ref["name"] == cronScheduleName && ref["controller"] == true {
			return true
		}
	}
	return false
}

func (k *K3s) CronRuns(ctx context.Context, name string) ([]model.CronRun, error) {
	_, uid, err := k.findCron(ctx, name)
	if err != nil {
		return nil, err
	}
	raw, err := k.run(ctx, []string{"get", "jobs", "-n", k.cronNamespace(name), "-l", cronLabel + "=" + model.CronKind, "-o", "json"}, nil)
	if err != nil {
		return nil, err
	}
	var list struct{ Items []map[string]any }
	if json.Unmarshal(raw, &list) != nil {
		return nil, model.Err("KUBERNETES_RESPONSE", "Invalid cron run history")
	}
	out := []model.CronRun{}
	for _, job := range list.Items {
		if k.cronOwned(job, name) && cronJobOwnedBy(job, uid) {
			out = append(out, describeCronRun(job))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func describeCronRun(job map[string]any) model.CronRun {
	meta, _ := job["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	status, _ := job["status"].(map[string]any)
	run := model.CronRun{Status: "pending", Trigger: "scheduled"}
	run.ID, _ = meta["name"].(string)
	run.CreatedAt, _ = meta["creationTimestamp"].(string)
	run.ScheduledAt, _ = annotations["batch.kubernetes.io/cronjob-scheduled-timestamp"].(string)
	if annotations["io.hostlier.trigger"] == "manual" {
		run.Trigger = "manual"
	}
	if active, _ := status["active"].(float64); active > 0 {
		run.Status = "running"
	}
	conditions, _ := status["conditions"].([]any)
	for _, raw := range conditions {
		c, _ := raw.(map[string]any)
		if c["status"] != "True" {
			continue
		}
		switch c["type"] {
		case "Complete":
			run.Status = "succeeded"
		case "Failed":
			run.Status = "failed"
		default:
			continue
		}
		run.FinishedAt, _ = c["lastTransitionTime"].(string)
		break
	}
	return run
}

func (k *K3s) CronLogs(ctx context.Context, name, run string, tail int) (string, error) {
	if !cronRunName.MatchString(run) {
		return "", model.Err("NOT_FOUND", "Cron run not found")
	}
	_, uid, err := k.findCron(ctx, name)
	if err != nil {
		return "", err
	}
	ns := k.cronNamespace(name)
	job, err := k.cronObject(ctx, "job", run, ns, name)
	if err != nil {
		return "", err
	}
	if !cronJobOwnedBy(job, uid) {
		return "", model.Err("NOT_FOUND", "Cron run not found")
	}
	raw, err := k.run(ctx, []string{"logs", "job/" + run, "-n", ns, "-c", "app", "--tail=" + strconv.Itoa(tail), "--limit-bytes=131072"}, nil)
	return string(raw), err
}
