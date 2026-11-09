# Approved native CPU autoscaling

For a public HTTP service that should stop when idle and wake on the next request, configure [idle scaling](guides/scale-and-monitor.md#sleep-an-idle-http-service).

A deployment can explicitly delegate bounded replica changes to Kubernetes on a stateless version 1 k3s application. Docker, persistent application volumes, independent workload groups, databases, memory-based autoscaling, vertical resizing, scale from zero and node/cloud capacity changes are excluded.

Add these fields to an otherwise valid `.hostlier` declaration:

```yaml
replicas: 1
resources:
  cpu: "0.1"
  memory: 64Mi
requests:
  cpu: "0.01"
  memory: 32Mi
autoscaling:
  minReplicas: 1
  maxReplicas: 3
  cpuTargetPercent: 65
```

An explicit durable ProductPolicy is required through the product declaration or an existing retained application policy. Both the retained policy and any newly declared policy constrain the maximum; the initial replica count must be within the positive delegated interval. CPU and memory requests and limits are mandatory. Requests must not exceed limits. The CPU metric is percentage of the CPU **request**, not the CPU limit or host capacity. `hostlier deploy`/`hostlier product deploy` present the policy, requests, maximum aggregate capacity, temporary overlap and persistence in the existing interactive approval. Merely editing the file or requesting a plan does not activate autoscaling. There is no implicit opt-in or approval bypass.

The native `autoscaling/v2` HPA changes at most one replica per 60 seconds upward, at most one replica per 300 seconds downward, and uses a 300-second downscale stabilization window. Limits are fixed for the approved revision. Unschedulable extra capacity stays pending; Hostlier never buys a node or changes database/storage capacity in response. Missing or partial metrics use Kubernetes' documented native behavior. Hostlier does not calculate a competing CPU recommendation loop.

Deployment handoff persists a pause before deleting the prior HPA. The new revision starts at the approved initial capacity. Delegation transfers only after workload readiness, edge cutover and HTTPS readiness. Its durable runtime journal binds policy, approved operation, exact Deployment UID and the admitted pod template. Replaying an immutable active operation does not reapply its replica field. Pending installation and revocation reconcile after controller restart; a paused policy cannot silently resume.

Manual `scale`, `stop`, `restart`, deletion and rollback revoke HPA ownership before capacity mutation. A manual scale or rollback omits delegation from the reviewed replacement. To resume, approve a fresh deployment containing the autoscaling policy. Explicit zero is never automatically woken. An unsuccessful HPA deletion prevents the conflicting capacity operation. Owner/UID/template/policy drift, visible foreign replica-manager or desired-count changes, and an externally deleted HPA pause delegation. Arbitrarily transient external writes can be overwritten by native HPA before Hostlier observes them; use the Hostlier CLI to obtain serialized revocation and manual precedence.

Activation requires fresh metrics-server samples. While Hostlier runs, a metrics API outage lasting five observed minutes revokes delegation; the native HPA retains its ordinary conservative behavior during the grace period. **HPA continues while Hostlier is down. There is no independently enforced expiry guarantee.** Native controller restart may lose its stabilization history. Neither Hostlier's lifecycle journal nor HPA status is a durable transcript of every native recommendation.

Runtime status separates approved initial/manual intent from live desired/ready replicas. It exposes the policy lifecycle journal and native HPA conditions, desired/current counts and `lastScaleTime`. Lifecycle evidence is bounded to 64 events and links to the existing approved operation audit; it contains no secret values.

The real local test uses pinned k3s v1.34.11+k3s1 and metrics-server v0.9.0, immutable nginx, CPU load inside its container, actual Deployment desired/ready counts, and certificate-validated isolated HTTPS. Actual controller and VM restart tests preserve a live delegation's exact HPA identity and approved bounds, restore certificate-validated HTTPS, retain the original authenticated Redis marker, and keep explicit zero stopped after a second VM restart. Real abrupt process exits at durable pause-before-delete and pending-target-before-apply boundaries recover under a restarted actual controller. Transient cluster API startup failures preserve valid bounded delegation; proven identity/template/policy drift revokes it. See [implementation evidence](autoscaling-progress.md). These versions are tested local versions, not an assertion that other installed Kubernetes versions are automatically supported.
