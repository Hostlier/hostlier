# Controller package deploy hooks

A project can request a controller package hook as a step in its deployment workflow. The CLI package planner can return the step in its project proposal, or an existing project can declare it directly in `.hostlier`. Local init, local packaging and controller installation are independent.

```yaml
workflow:
  steps:
    - id: release
      package: example
      hook: project-deploy
      phase: beforeDeploy
      secrets:
        runtimeToken: example-runtime-token
```

Install the executable on the target controller using the ordinary package installation flow. Its v1 `describe` capability must include `"hooks":["project-deploy"]`. The name selects only a controller-installed executable. Registering a hook on the CLI has no effect on controller installations. Hosts can use `needs`, `inputs` and `workflow.bindings` on this step using the existing [package workflow contract](../../packages/WORKFLOWS.md).

The controller executes `<package-executable> project-deploy` with one v1 workflow request on stdin. The package responds on stdout with the matching v1 workflow response. Go packages can dispatch this argument through `packagekit.Serve`. For typed packages, `Definition.Hooks` declares the entry point in `describe` and `Call.Request.Project` holds the deployment spec.

| Request action | When it runs | Successful response fields |
| --- | --- | --- |
| `plan` | During controller plan review, before approval. Describe the work without making external changes. | `{"version":1,"prepared":{"summary":["Bind this release"],"payload":{"release":"…"}}}` |
| `apply` | After approval, before workload rollout. The controller supplies the original `prepared` payload and pinned secret revisions. | `{"version":1,"result":{"state":{"receipt":"…"},"outputs":{"ready":"yes"}}}` |
| `inspect` | On an explicit recovery request following an uncertain apply. Observe the outcome without repeating the effect. | `{"version":1,"inspection":{"status":"not_started"}}`, `{"version":1,"inspection":{"status":"uncertain"}}`, or `completed` with the verified `result`. |

Requests carry `version`, `action`, `key`, `step`, `host`, and `project`. The last field is the controller-resolved project, including the pinned image digest and canonical route. Apply and inspect receive the same reviewed project. `secrets` contains values for the step’s explicitly declared secret aliases. `inputs` contains resolved outputs of declared predecessors when available. The server adds its trusted runtime context in `host`. Package diagnostics and opaque payloads stay out of review summaries.

The reviewed plan pins the executable digest. Changing the executable invalidates that approval. Use the request `key` to track the external effect for this operation. Hostlier records a successful step before continuing the rollout. If delivery of the result is uncertain, Hostlier blocks replay until an operator inspects and reconciles the original attempt. A separately approved deployment has a new hook key. Packages managing longer-lived grants should look up and reuse them by application and permission scope. Deployment-only scoped credentials retain the existing restrictions on provisioning workflows.
