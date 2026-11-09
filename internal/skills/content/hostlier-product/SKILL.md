---
name: hostlier-product
description: Plan or deploy a declarative Hostlier product composed from package workflow steps.
---

# Hostlier product

Use `hostlier product plan --file product.yaml --server production` to expand and review a declarative product, then `hostlier product deploy --file product.yaml --server production` when deployment is requested.

A product file is a single version 1 YAML document with `product`, `inputs`, optional `secretRefs`, and `app`. The app must not declare a second workflow. Inspect installed package capability schemas with `hostlier package describe NAME` on the server when authoring package composition. Use secret references rather than raw credentials in public inputs.

This CLI consumes declarative Hostlier YAML, not the separate TypeScript hosting SDK's `defineProduct` source. Preserve an existing valid declaration and validate required inputs, dependency order and output bindings. Product deployment uses the ordinary hardware-signed exact-plan approval flow; planning does not authorize provisioning. Let the user provide required approval and track the resulting operation to completion.

The controller enforces human authority. When a request is pending, give its printed code to the human for `hostlier approve CODE --key FILE` from their trusted terminal; email enrollment and an agent API credential cannot sign it.
