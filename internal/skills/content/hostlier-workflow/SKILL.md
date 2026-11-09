---
name: hostlier-workflow
description: Inspect uncertain Hostlier package workflow outcomes and reconcile verified recovery evidence.
---

# Hostlier workflow

Inspect retained work before retrying external provisioning:

```sh
hostlier workflow list APP --server production
hostlier workflow inspect APP STEP --server production
```

Use the actual step IDs returned by the controller. An uncertain result means the external effect is not established; do not assume failure or replay a provider call that may already have succeeded.

When inspection provides verified recovery evidence, `hostlier workflow reconcile APP STEP` presents it and requires fresh hardware approval. Reconciliation updates retained recovery metadata; it does not replay the external work. The controller rejects still-uncertain outcomes and rechecks the evidence fingerprint. Let the user supply approval and preserve the unresolved status if evidence remains inconclusive.

The controller enforces human authority. When a request is pending, give its printed code to the human for `hostlier approve CODE --key FILE` from their trusted terminal; email enrollment and an agent API credential cannot sign it.
