package runtime

import (
	"context"
	"time"

	"hostlier/internal/model"
)

// k3sDeployment owns rollback state for one immutable Kubernetes deployment.
type k3sDeployment struct {
	runtime               *K3s
	ctx                   context.Context
	spec                  model.Project
	dc                    model.DeployContext
	s                     state
	route                 model.Route
	old                   appRecord
	hasOld                bool
	version, namespace    string
	oldStopped, activated bool
	manifests             []map[string]any
	pullConfig            []byte
	out                   model.Deployment
}

// prepare verifies immutable inputs, namespace ownership and route availability.
func (d *k3sDeployment) prepare() error {
	// Reject unsupported edge transports before changing Kubernetes resources.
	if err := d.runtime.base.validateEdgeFeatures(d.ctx, d.spec); err != nil {
		return err
	}
	k := d.runtime
	ctx := d.ctx
	var e error

	// Validate immutable inputs and explicit backend capability before Kubernetes mutations.
	if err := d.validateInputs(); err != nil {
		return err
	}
	d.pullConfig, e = k.k3sPullConfig(ctx, d.spec)
	// Resolve registry logins before any mutation so credential failures change nothing.
	if e != nil {
		return e
	}
	d.s, e = k.base.read()
	// Do not deploy against unreadable runtime metadata.
	if e != nil {
		return e
	}
	d.route, e = workloadRoute(d.spec, k.options.Hostname)
	// Require a canonical application route outside the controller hostname.
	if e != nil {
		return e
	}
	// Reject routes overlapping another tracked application.
	if err := k.base.validateEndpointRoutes(ctx, d.s, d.spec); err != nil {
		return err
	}
	// Verify each resource namespace before granting application egress.
	if err := d.validateDependencies(); err != nil {
		return err
	}
	// Import approved local artifacts directly into containerd before scheduling.
	if e = k.importGroupArtifacts(ctx, d.spec); e != nil {
		return e
	}
	// Require the application's owned namespace before creating versioned objects.
	if e = k.namespaceReady(ctx, d.spec.Name); e != nil {
		return e
	}
	d.old, d.hasOld = d.s.Apps[d.spec.Name]
	d.version = "app-" + shortHash(d.dc.OperationID)
	d.namespace = k.namespace(d.spec.Name)
	return d.prepareOwnedTransition()
}

// rollback reinstates the old writer, policy, service and edge after failure.
func (d *k3sDeployment) rollback() {
	k := d.runtime

	cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Remove any candidate HPA before candidate deletion or restoration of serving capacity.
	if !d.rollbackAutoscaling(cleanup) {
		return
	}
	// Restore the source only after version-one candidate pods have authoritatively disappeared.
	if !d.rollbackCandidate(cleanup) {
		return
	}
	// Restart the previous persistent-volume writer if this attempt stopped it.
	if d.oldStopped {
		_ = k.scale(cleanup, d.namespace, d.old.RuntimeID, model.DesiredReplicas(d.old.Spec))
		_ = k.ready(cleanup, d.namespace, d.old.RuntimeID)
	}
	// Reinstate the old policy and service when an earlier version existed.
	if d.hasOld {
		// Network recovery never needs to rematerialize retained file secrets.
		_ = k.restoreIsolationPolicy(cleanup, d.old)
		_, _ = k.groupService(cleanup, d.old.Spec, d.old.RuntimeID)
		d.s.Apps[d.spec.Name] = d.old
	} else {
		_ = k.remove(cleanup, "service", "app", d.namespace)
		delete(d.s.Apps, d.spec.Name)
	}
	// A failed attempt must not leave newly written credentials behind for a version that never used them.
	if d.pullConfig != nil && !d.old.PullSecret {
		_ = k.remove(cleanup, "secret", k3sPullSecret, d.namespace)
	}
	d.restoreRollbackState(cleanup)
}

// stopOldWriter builds manifests and stops the prior version before reusing persistent claims.
func (d *k3sDeployment) stopOldWriter() error {
	k := d.runtime
	ctx := d.ctx
	var e error

	d.manifests, e = k.K3sManifests(d.spec, d.dc)
	// Do not stop a working previous version if manifest generation failed.
	if e != nil {
		return e
	}
	d.manifests = k.withPullSecret(d.spec, d.manifests, d.pullConfig)
	// Stop the old version before a different candidate mounts persistent storage.
	if d.hasOld && d.old.RuntimeID != d.version && groupHasVolumes(d.spec) {
		// Abort if the previous writer cannot be scaled down.
		if e = k.scale(ctx, d.namespace, d.old.RuntimeID, 0); e != nil {
			return e
		}
		d.oldStopped = true
		// Wait for old pods to disappear before permitting another volume writer.
		if _, e = k.run(ctx, []string{"wait", "--for=delete", "pods", "-n", d.namespace, "-l", "io.hostlier.version=" + d.old.RuntimeID, "--timeout=60s"}, nil); e != nil {
			return e
		}
	}
	return nil
}

// applyManifests applies isolation and workload manifests before waiting for rollout readiness.
func (d *k3sDeployment) applyManifests() error {
	k := d.runtime
	ctx := d.ctx
	var e error

	// Apply isolation first, followed by approved versioned workload objects.
	for _, manifest := range d.manifests {
		// Immutable replay must not overwrite the replica field owned by an active HPA.
		if manifest["kind"] == "Deployment" && d.old.OperationID == d.dc.OperationID && d.old.Autoscaling != nil {
			continue
		}
		// Any rejected manifest triggers transactional cleanup and old-version restoration.
		if e = k.apply(ctx, manifest); e != nil {
			return e
		}
	}
	// Do not publish a service before the candidate becomes HTTP ready.
	if e = k.ready(ctx, d.namespace, d.version); e != nil {
		return rolloutReadinessError(e)
	}
	return nil
}

// activate publishes the private service and commits verified HTTPS routing.
func (d *k3sDeployment) activate() error {
	k := d.runtime
	ctx := d.ctx
	var e error

	upstream, e := k.groupService(ctx, d.spec, d.version)
	// Require a private allocated service endpoint before edge activation.
	if e != nil {
		return e
	}
	d.out = model.Deployment{Endpoints: endpointURLs(d.spec), Name: d.spec.Name, URL: d.route.URL, RuntimeID: d.version, ImageDigest: d.spec.Image, Status: "running"}
	// Keep a zero-count route reserved but unavailable.
	if model.DesiredReplicas(d.spec) == 0 {
		d.out.Status = "stopped"
	}
	d.s.Apps[d.spec.Name] = appRecord{EndpointUpstreams: clusterEndpointUpstreams(d.spec, upstream), PreviousRuntimeID: d.previousID(), Deployment: d.out, Spec: d.spec, Upstream: upstream, OperationID: d.dc.OperationID, DependencyNetworks: d.dc.DependencyNetworks, PullSecret: d.pullConfig != nil}
	// Preserve native replica ownership during replay of the already committed operation.
	if d.old.OperationID == d.dc.OperationID {
		app := d.s.Apps[d.spec.Name]
		app.Autoscaling = d.old.Autoscaling
		d.s.Apps[d.spec.Name] = app
	}
	// Keep rollback active if the private edge cannot load the new upstream.
	if e = k.base.caddy(ctx, d.s); e != nil {
		return e
	}
	d.activated = true
	// Require valid public HTTPS readiness before saving the new deployment.
	if model.DesiredReplicas(d.spec) > 0 {
		// Verify public health only when capacity is expected.
		if e = k.base.publicEndpointsReady(ctx, d.spec); e != nil {
			return e
		}
	}
	// A failed durable save must trigger rollback rather than report completion.
	if e = k.base.save(d.s); e != nil {
		return e
	}
	return nil
}

// cleanup removes superseded versioned workload objects after activation.
func (d *k3sDeployment) cleanup() error {
	k := d.runtime
	ctx := d.ctx

	// A deployment that no longer needs registry credentials removes the stale namespace copy.
	if d.pullConfig == nil && d.old.PullSecret {
		_ = k.remove(ctx, "secret", k3sPullSecret, d.namespace)
	}
	// Remove only the superseded version after successful activation.
	previous := d.previousID()
	// Drain only a distinct retained source version.
	if previous != "" && previous != d.version {
		// Retain a pending source marker if drain failed or the controller stopped.
		if e := k.drain(ctx, d.namespace, previous); e == nil {
			app := d.s.Apps[d.spec.Name]
			app.PreviousRuntimeID = ""
			d.s.Apps[d.spec.Name] = app
			_ = k.base.save(d.s)
		}
	}
	return nil
}

// validateDependencies checks each controller-resolved namespace belongs to this server.
func (d *k3sDeployment) validateDependencies() error {
	k := d.runtime
	ctx := d.ctx

	// Inspect each controller-resolved dependency namespace before trusting it.
	for _, ns := range d.dc.DependencyNetworks {
		obj, e := k.get(ctx, "namespace", ns, "")
		// Propagate namespace lookup failures before changing egress permissions.
		if e != nil {
			return e
		}
		// Require an existing namespace carrying this server's ownership labels.
		if obj == nil || !k.owned(obj) {
			return model.Err("OWNERSHIP", "Dependency namespace is not owned by this server")
		}
	}
	return nil
}

// previousID retains source identity across accepted-operation replay after cutover.
func (d *k3sDeployment) previousID() string {
	// An already committed version may still need to drain its source.
	if d.old.OperationID == d.dc.OperationID {
		return d.old.PreviousRuntimeID
	}
	return d.old.RuntimeID
}

// drain scales retained source capacity down and bounds graceful pod termination.
func (k *K3s) drain(ctx context.Context, namespace, version string) error {
	// Stop scheduling source replicas only after new capacity and routing are ready.
	if err := k.scale(ctx, namespace, version, 0); err != nil {
		return err
	}
	_, err := k.run(ctx, []string{"wait", "--for=delete", "pods", "-n", namespace, "-l", "io.hostlier.version=" + version, "--timeout=60s"}, nil)
	return err
}

// validateInputs rejects unresolved images and unsupported security contracts before scheduling.
func (d *k3sDeployment) validateInputs() error {
	// Node-local security policy distribution is not implemented by the cluster adapter.
	if hasContainerSecurity(d.spec) {
		return model.Err("UNSUPPORTED_SECURITY", "This container security contract currently requires Docker")
	}
	// The operation identity also defines the immutable manifest revision.
	if !digestPattern.MatchString(d.spec.Image) || d.dc.OperationID == "" {
		return model.Err("UNPINNED_IMAGE", "Deployment requires approved immutable image and operation ID")
	}
	return nil
}
