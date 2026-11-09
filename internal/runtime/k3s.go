package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"hostlier/internal/artifact"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// K3s adapts owned Kubernetes objects while sharing private edge and durable runtime metadata.
type K3s struct {
	base    *Runtime
	options Options
}

// newK3s constructs an explicitly configured local cluster adapter.
func newK3s(o Options) (model.Runtime, error) {
	copy := o
	copy.Type = "docker"
	adapter, e := New(copy)
	// Require valid private edge configuration from the shared runtime initializer.
	if e != nil {
		return nil, e
	}
	base := adapter.(*Runtime)
	base.stateFilename = "runtime-k3s.json"
	// Use the pinned distribution's bundled kubectl command by default.
	if o.KubeBinary == "" {
		o.KubeBinary = "/usr/local/bin/k3s"
	}
	// Address the installation's explicit local kubeconfig instead of ambient user contexts.
	if o.Kubeconfig == "" {
		o.Kubeconfig = "/etc/rancher/k3s/k3s.yaml"
	}
	// Give cluster scheduling its explicit default readiness window.
	if o.HealthTimeout == 0 {
		o.HealthTimeout = 60 * time.Second
	}
	return &K3s{base: base, options: o}, nil
}

// Name reports the stable Kubernetes backend identifier.
func (k *K3s) Name() string { return "k3s" }

// SupportsDependencies reports support for namespace-scoped private resource bindings.
func (k *K3s) SupportsDependencies() bool { return true }

// namespace derives a server-owned application namespace within the DNS label limit.
func (k *K3s) namespace(app string) string {
	name := "hostlier-" + k.base.owner + "-" + app
	// Hash long application identities while retaining a recognizable bounded prefix.
	if len(name) > 63 {
		name = name[:46] + "-" + shortHash(name)
	}
	return name
}

// run executes kubectl with explicit cluster placement and bounded captured output.
func (k *K3s) run(ctx context.Context, args []string, input []byte) ([]byte, error) {
	fixed := append([]string{"kubectl", "--kubeconfig=" + k.options.Kubeconfig, "--context=default", "--request-timeout=20s"}, args...)
	// Tests may inject a command runner without invoking a local cluster.
	if k.options.KubeRun != nil {
		return k.options.KubeRun(ctx, fixed, input)
	}
	bounded, cancel := context.WithTimeout(ctx, k.options.HealthTimeout+30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, k.options.KubeBinary, fixed...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "KUBECTL_KUBERC=false"}
	cmd.Stdin = bytes.NewReader(input)
	out, e := cmd.Output()
	// Normalize command failures without exposing kubectl output that may contain secrets.
	if e != nil {
		return nil, model.Err("KUBERNETES_COMMAND", "Kubernetes operation failed; inspect protected cluster diagnostics")
	}
	// Reject unexpectedly large command output before decoding object data.
	if len(out) > 2*1024*1024 {
		return nil, model.Err("KUBERNETES_RESPONSE", "Kubernetes response exceeds limit")
	}
	return out, nil
}

// get obtains one optionally namespaced object, treating absence as nil.
func (k *K3s) get(ctx context.Context, kind, name, namespace string) (map[string]any, error) {
	args := []string{"get", kind, name, "--ignore-not-found", "-o", "json"}
	// Replica-manager ownership is required to distinguish native HPA from external manual drift.
	if kind == "deployment" {
		args = append(args, "--show-managed-fields=true")
	}
	// Append namespace placement only for namespaced objects.
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	raw, e := k.run(ctx, args, nil)
	// Do not decode failed command responses.
	if e != nil {
		return nil, e
	}
	// An empty ignore-not-found response represents an absent object.
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var object map[string]any
	// Reject malformed Kubernetes JSON before ownership inspection.
	if json.Unmarshal(raw, &object) != nil {
		return nil, model.Err("KUBERNETES_RESPONSE", "Invalid Kubernetes JSON")
	}
	return object, nil
}

// owned verifies this server's managed ownership labels on Kubernetes metadata.
func (k *K3s) owned(object map[string]any) bool {
	metadata, _ := object["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	return labels["io.hostlier.managed"] == "true" && labels["io.hostlier.owner"] == k.base.owner
}

// apply refuses unowned preexisting objects before applying a manifest.
func (k *K3s) apply(ctx context.Context, manifest map[string]any) error {
	kind := manifest["kind"].(string)
	metadata := manifest["metadata"].(map[string]any)
	name := metadata["name"].(string)
	namespace, _ := metadata["namespace"].(string)
	existing, e := k.get(ctx, kind, name, namespace)
	// Require a successful lookup before deciding whether adoption is safe.
	if e != nil {
		return e
	}
	// Never replace an existing object belonging to another owner.
	if existing != nil && !k.owned(existing) {
		return model.Err("OWNERSHIP", "Refusing to adopt unowned Kubernetes resource")
	}
	raw, _ := json.Marshal(manifest)
	_, e = k.run(ctx, []string{"apply", "--server-side", "--field-manager=hostlier", "-f", "-"}, raw)
	return e
}

// metadata supplies application-scoped ownership and explicit namespace placement.
func (k *K3s) metadata(app, name string) map[string]any {
	return map[string]any{"name": name, "namespace": k.namespace(app), "labels": k.base.labels(app)}
}

// namespaceReady creates or verifies the application's owned namespace.
func (k *K3s) namespaceReady(ctx context.Context, app string) error {
	return k.apply(ctx, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": k.namespace(app), "labels": k.base.labels(app)}})
}

// scale updates the replicas of an explicitly namespaced deployment.
func (k *K3s) scale(ctx context.Context, namespace, name string, replicas int) error {
	_, e := k.run(ctx, []string{"scale", "deployment", name, "-n", namespace, "--replicas=" + strconv.Itoa(replicas)}, nil)
	return e
}

// ready waits for the candidate rollout using the configured readiness timeout.
func (k *K3s) ready(ctx context.Context, namespace, name string) error {
	_, e := k.run(ctx, []string{"rollout", "status", "deployment/" + name, "-n", namespace, "--timeout=" + strconv.Itoa(int(k.options.HealthTimeout.Seconds())) + "s"}, nil)
	// Preserve observed scheduling and memory failures in operation diagnostics.
	if e != nil {
		return k.rolloutFailure(ctx, namespace, name, e)
	}
	return nil
}

// remove verifies object ownership before deleting an optionally namespaced object.
func (k *K3s) remove(ctx context.Context, kind, name, namespace string) error {
	obj, e := k.get(ctx, kind, name, namespace)
	// Do not delete an object whose lookup failed.
	if e != nil {
		return e
	}
	// Already absent objects make deletion idempotent.
	if obj == nil {
		return nil
	}
	// Refuse deletion of unowned Kubernetes objects.
	if !k.owned(obj) {
		return model.Err("OWNERSHIP", "Refusing to delete unowned Kubernetes resource")
	}
	args := []string{"delete", kind, name, "--ignore-not-found", "--wait=true", "--timeout=60s"}
	// Include namespace placement only when deleting namespaced resources.
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	_, e = k.run(ctx, args, nil)
	return e
}

// InspectImage resolves for the controller platform without consulting a separate Docker image cache.
func (k *K3s) InspectImage(ctx context.Context, image string) (model.ImageInfo, error) {
	// Artifact verification uses the same retained store as Docker.
	if artifact.IsImage(image) {
		return k.base.inspectArtifact(image)
	}
	info, err := k.base.resolveRegistryManifest(ctx, image, model.ImageInfo{Image: image, Warnings: []string{}})
	return info, registryImageContext(image, err)
}

// K3sManifests creates a versioned candidate, persistent claims, secret and default
// app isolation. No NodePorts, host ports, or privileged workload options exist.
func (k *K3s) K3sManifests(spec model.Project, dc model.DeployContext) ([]map[string]any, error) {
	// Manifest-only callers must not silently lose declared kernel confinement either.
	if hasContainerSecurity(spec) {
		return nil, model.Err("UNSUPPORTED_SECURITY", "This container security contract currently requires Docker")
	}
	ns := k.namespace(spec.Name)
	version := "app-" + shortHash(dc.OperationID)
	labels := k.base.labels(spec.Name)
	labels["io.hostlier.version"] = version
	manifests := []map[string]any{}
	mounts, volumes := []any{}, []any{}
	// Describe each named persistent volume as a local-path claim and matching container mount.
	for name, v := range spec.Volumes {
		claim := "data-" + name
		manifests = append(manifests, map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": k.metadata(spec.Name, claim), "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "storageClassName": "local-path", "resources": map[string]any{"requests": map[string]string{"storage": v.Size}}}})
		mounts = append(mounts, map[string]any{"name": name, "mountPath": v.Mount})
		volumes = append(volumes, map[string]any{"name": name, "persistentVolumeClaim": map[string]string{"claimName": claim}})
	}
	env := []any{}
	// Encode literal environment values directly in the application container.
	for name, value := range spec.Env {
		env = append(env, map[string]string{"name": name, "value": value})
	}
	container := map[string]any{"name": "app", "image": spec.Image, "ports": []any{map[string]any{"containerPort": spec.Port}}, "env": env, "volumeMounts": mounts}
	// Store resolved secret bindings in a version-scoped Kubernetes Secret.
	if len(dc.Secrets) > 0 {
		manifests = append(manifests, map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": k.metadata(spec.Name, version), "type": "Opaque", "stringData": dc.Secrets})
		container["envFrom"] = []any{map[string]any{"secretRef": map[string]string{"name": version}}}
	}
	path := healthPath(spec)
	container["readinessProbe"] = map[string]any{"httpGet": map[string]any{"path": path, "port": spec.Port}, "periodSeconds": 1, "timeoutSeconds": 2, "failureThreshold": 60}
	// Add only explicitly approved CPU and memory limits.
	if spec.Resources != nil {
		limits := map[string]string{}
		// Preserve an omitted CPU bound rather than inventing a quota.
		if spec.Resources.CPU != "" {
			limits["cpu"] = spec.Resources.CPU
		}
		// Preserve an omitted memory bound rather than inventing a quota.
		if spec.Resources.Memory != "" {
			limits["memory"] = spec.Resources.Memory
		}
		allocation := map[string]any{"limits": limits}
		// Explicit requests make HPA utilization independent of admission defaults.
		if spec.Requests != nil {
			allocation["requests"] = map[string]string{"cpu": spec.Requests.CPU, "memory": spec.Requests.Memory}
		}
		container["resources"] = allocation
	}
	manifests = append(manifests, map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": k.metadata(spec.Name, version), "spec": map[string]any{"replicas": model.DesiredReplicas(spec), "strategy": map[string]string{"type": "Recreate"}, "selector": map[string]any{"matchLabels": map[string]string{"io.hostlier.version": version}}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"nodeSelector": map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": goruntime.GOARCH}, "automountServiceAccountToken": false, "containers": []any{container}, "volumes": volumes}}}})
	policy, err := k.isolationPolicy(spec, dc.DependencyNetworks)
	// Dependency ownership remains mandatory before any candidate can be scheduled.
	if err != nil {
		return nil, err
	}
	_ = ns
	manifests = append([]map[string]any{policy}, manifests...)
	return k.extendGroupManifests(spec, dc, manifests)
}

// service applies the owned private service and returns its allocated upstream address.
func (k *K3s) service(ctx context.Context, spec model.Project, version string) (string, error) {
	manifest := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": k.metadata(spec.Name, "app"), "spec": map[string]any{"type": "ClusterIP", "selector": map[string]string{"io.hostlier.version": version}, "ports": servicePorts(spec)}}
	// Do not inspect service allocation before the owned manifest is applied.
	if e := k.apply(ctx, manifest); e != nil {
		return "", e
	}
	service, e := k.get(ctx, "service", "app", k.namespace(spec.Name))
	// Propagate allocation lookup failures before activating routing.
	if e != nil {
		return "", e
	}
	serviceSpec, _ := service["spec"].(map[string]any)
	ip, _ := serviceSpec["clusterIP"].(string)
	// Require a concrete private ClusterIP rather than a headless endpoint.
	if ip == "" || ip == "None" {
		return "", model.Err("KUBERNETES_SERVICE", "Private service has no allocated endpoint")
	}
	return ip + ":" + strconv.Itoa(spec.Port), nil
}

// Deploy activates an approved immutable Kubernetes workload with rollback on failure.
func (k *K3s) Deploy(ctx context.Context, spec model.Project, dc model.DeployContext) (out model.Deployment, err error) {
	k.base.mu.Lock()
	defer k.base.mu.Unlock()
	// Reject unsupported storage concurrency before namespace mutation.
	if err = validateReplicas(spec, "k3s"); err != nil {
		return out, err
	}
	// Validate delegation constraints before any namespace, image or workload mutation.
	if spec.Autoscaling != nil {
		// Require a successful fixture operation or authoritative observation before continuing safety assertions.
		if _, err = config.ValidateProject(spec); err != nil {
			return out, err
		}
	}
	d := &k3sDeployment{runtime: k, ctx: ctx, spec: spec, dc: dc}
	// Validate immutable inputs and ownership before workload mutation.
	if err = d.prepare(); err != nil {
		return out, err
	}
	// Retain bounded transactional rollback until the new deployment is committed.
	defer func() {
		// Only failed attempts restore the previous version and edge.
		if err != nil {
			d.rollback()
		}
	}()
	// Apply volume-writer transition, candidate readiness and edge activation in order.
	for _, stage := range []func() error{d.stopOldWriter, d.applyManifests, d.activate, d.activateAutoscaling, d.cleanup, d.completeTransition} {
		// Stop at the first failed stage so rollback sees the error.
		if err = stage(); err != nil {
			return d.out, err
		}
	}
	return d.out, nil
}

// Action changes only the owned application's Kubernetes lifecycle and edge state.
func (k *K3s) Action(ctx context.Context, name, action string, ac model.ActionContext) error {
	k.base.mu.Lock()
	defer k.base.mu.Unlock()
	s, e := k.base.read()
	// Do not mutate a workload without readable durable ownership metadata.
	if e != nil {
		return e
	}
	app, ok := s.Apps[name]
	// Deletion may address an already deleted application whose data was retained.
	if !ok && action == "delete" {
		app, ok = s.Retained[name]
	}
	// Reject applications absent from both relevant ownership collections.
	if !ok {
		return model.Err("APP_NOT_FOUND", "Application not found")
	}
	app.IdleSleeping = false
	// Lifecycle start and restart cannot override approved zero desired replicas.
	if zeroLifecycle(app.Spec, action) {
		return model.Err("SCALE_APPROVAL_REQUIRED", "Scale replicas above zero with an approved scaling plan")
	}
	namespace := k.namespace(name)
	existing, e := k.get(ctx, "namespace", namespace, "")
	// Require namespace lookup before checking server ownership.
	if e != nil {
		return e
	}
	// Refuse lifecycle operations in an unowned namespace.
	if existing != nil && !k.owned(existing) {
		return model.Err("OWNERSHIP", "Namespace belongs to another owner")
	}
	// Do not update the edge or durable state after a failed workload action.
	if err := k.fencedLifecycleAction(ctx, name, namespace, action, app, s, ac); err != nil {
		return err
	}
	return k.commitLifecycle(ctx, action, app.URL, s)
}

// commitLifecycle publishes routing and verifies usable HTTPS before persisting a lifecycle transition.
func (k *K3s) commitLifecycle(ctx context.Context, action, target string, s state) error {
	// Keep routing consistent with the resulting running or stopped status.
	if e := k.base.caddy(ctx, s); e != nil {
		return e
	}
	// Rollout readiness can precede service endpoint propagation; verify the active HTTPS route before success.
	if action == "start" || action == "restart" {
		// Preserve durable status when the routed workload remains unavailable.
		if e := k.base.publicReady(ctx, target); e != nil {
			return e
		}
	}
	return k.base.save(s)
}

// Logs reads bounded recent logs from the explicitly namespaced owned deployment.
func (k *K3s) Logs(ctx context.Context, name string) (string, error) {
	s, e := k.base.read()
	// Preserve state-read failures before selecting a workload.
	if e != nil {
		return "", e
	}
	app, ok := s.Apps[name]
	// Reject requests for untracked applications.
	if !ok {
		return "", model.Err("APP_NOT_FOUND", "Application not found")
	}
	raw, e := k.run(ctx, []string{"logs", "deployment/" + app.RuntimeID, "-n", k.namespace(name), "--tail=" + strconv.Itoa(model.LogTail(ctx)), "--limit-bytes=65536"}, nil)
	return string(raw), e
}

// Doctor probes the explicitly configured cluster's local readiness endpoint.
func (k *K3s) Doctor(ctx context.Context) (any, error) {
	raw, e := k.run(ctx, []string{"get", "--raw=/readyz"}, nil)
	return map[string]any{"runtime": "k3s", "ready": e == nil && strings.TrimSpace(string(raw)) == "ok"}, e
}

// Status reports observed rollout state without exposing secret manifests.
func (k *K3s) Status(ctx context.Context, name string) (map[string]any, error) {
	s, e := k.base.read()
	// Require durable application metadata before querying the cluster.
	if e != nil {
		return nil, e
	}
	app, ok := s.Apps[name]
	// Reject status requests for untracked applications.
	if !ok {
		return nil, model.Err("APP_NOT_FOUND", "Application not found")
	}
	deployment, e := k.get(ctx, "deployment", app.RuntimeID, k.namespace(name))
	// Propagate deployment lookup failure before interpreting rollout counters.
	if e != nil {
		return nil, e
	}
	status, _ := deployment["status"].(map[string]any)
	out := map[string]any{"runtimeId": app.RuntimeID, "replicas": status["replicas"], "readyReplicas": status["readyReplicas"], "components": k.k3sComponents(ctx, app), "autoscaling": app.Autoscaling}
	app.addIdleStatus(out)
	// Native conditions and lastScaleTime remain observation, separate from approved manual intent.
	if app.Autoscaling != nil {
		hpa, err := k.get(ctx, "horizontalpodautoscaler", "autoscaler", k.namespace(name))
		// Require a successful fixture operation or authoritative observation before continuing safety assertions.
		if err != nil {
			return nil, err
		}
		out["autoscalingStatus"] = hpa["status"]
	}
	return out, nil
}

// applyLifecycleAction changes the owned workload and updates its tracked status.
func (k *K3s) applyLifecycleAction(ctx context.Context, name, namespace, action string, app appRecord, s state, ac model.ActionContext) error {
	var e error

	// Dispatch only supported lifecycle transitions after namespace ownership validation.
	switch action {
	// Deletion follows its own retention and optional purge workflow.
	case "delete":
		return k.deleteWorkload(ctx, name, namespace, app, s, ac)
	// Stopping removes the workload's running replica while retaining data.
	case "stop":
		// Do not mark the application stopped if scaling failed.
		if e = k.scale(ctx, namespace, app.RuntimeID, 0); e != nil {
			return e
		}
		app.Status = "stopped"
		s.Apps[name] = app
	// Starting and restarting require a ready running deployment.
	case "start", "restart":
		// Restart replaces running pods rather than only setting the replica count.
		if action == "restart" {
			// A rejected restart must preserve the recorded application state.
			if _, e = k.run(ctx, []string{"rollout", "restart", "deployment/" + app.RuntimeID, "-n", namespace}, nil); e != nil {
				return e
			}
		} else if e = k.scale(ctx, namespace, app.RuntimeID, model.DesiredReplicas(app.Spec)); e != nil { // Start restores the deployment's single replica.
			return e
		}
		// Require rollout readiness before publishing running status.
		if e = k.readyGroup(ctx, app); e != nil {
			return e
		}
		app.Status = "running"
		s.Apps[name] = app
	// Reject unsupported lifecycle action names.
	default:
		return model.Err("ACTION", "Unknown lifecycle action")
	}
	return nil
}

// deleteWorkload withdraws routing before removing workload objects and optional data.
func (k *K3s) deleteWorkload(ctx context.Context, name, namespace string, app appRecord, s state, ac model.ActionContext) error {
	var e error

	delete(s.Apps, name)
	// Withdraw the application route before deleting backend objects.
	if e = k.base.caddy(ctx, s); e != nil {
		return e
	}
	// Remove versioned workload objects while leaving persistent claims in place.
	for _, kind := range []string{"deployment", "secret", "service"} {
		resource := app.RuntimeID
		// The stable private service has a fixed name independent of the version.
		if kind == "service" {
			resource = "app"
		}
		// Skip an absent version identifier on retained application records.
		if resource != "" {
			// Stop deletion if any owned object cannot be removed.
			if e = k.remove(ctx, kind, resource, namespace); e != nil {
				return e
			}
		}
	}
	// Registry credentials are removed with the workload; only data claims are retained.
	if app.PullSecret {
		// Keep the retained record consistent with a credential that could not be removed.
		if e = k.remove(ctx, "secret", k3sPullSecret, namespace); e != nil {
			return e
		}
		app.PullSecret = false
	}
	app.Status = "deleted"
	s.Retained[name] = app
	// Delete the data-bearing namespace only after explicit purge approval.
	if ac.Purge {
		// Do not discard retained metadata if namespace purge fails.
		if e = k.remove(ctx, "namespace", namespace, ""); e != nil {
			return e
		}
		delete(s.Retained, name)
	}
	return nil
}

// isolationPolicy builds only networking metadata, independent from file and credential materialization.
func (k *K3s) isolationPolicy(spec model.Project, dependencies []string) (map[string]any, error) {
	egress := []any{map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, "ports": []any{map[string]any{"protocol": "UDP", "port": 53}, map[string]any{"protocol": "TCP", "port": 53}}}, map[string]any{"to": []any{map[string]any{"ipBlock": map[string]any{"cidr": "0.0.0.0/0", "except": []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16"}}}}}}
	// Grant egress only to controller-resolved resource namespaces.
	for _, namespace := range dependencies {
		// Reject dependency namespaces outside this server's ownership scope.
		if !strings.HasPrefix(namespace, "hostlier-"+k.base.owner+"-resource-") {
			return nil, model.Err("OWNERSHIP", "Dependency namespace belongs to another server")
		}
		egress = append(egress, map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": namespace}}}}})
	}
	return map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": k.metadata(spec.Name, "app-isolation"), "spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{}, "egress": egress}}, nil
}
