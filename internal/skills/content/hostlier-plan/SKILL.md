---
name: hostlier-plan
description: Review a Hostlier deployment plan without applying the deployment.
---

# Hostlier plan

Inspect the existing `.hostlier` file and validate it locally, then request the selected server's plan:

```sh
hostlier validate --file .hostlier
hostlier plan --file .hostlier --server production
```

Use `--route app.example.com` only for public workloads when a route override is intended. For noninteractive planning, provide a public route in the file or flag so the CLI need not prompt for it. Private services and workers must not get a public route.

Planning does not apply the deployment, but native artifact preparation can package and upload immutable artifacts and the controller can retain plan state. Review image identity, route, resource bindings, storage, capacity and maintenance implications. Planning is not authorization to deploy. If deployment is requested, the CLI still requires review and approval of its exact plan.
