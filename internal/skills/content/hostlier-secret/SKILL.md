---
name: hostlier-secret
description: Manage Hostlier secret revisions and references without exposing secret values.
---

# Hostlier secret

Inspect metadata with `hostlier secret list --server production`. Set a value using the hidden prompt, protected stdin, or an existing environment variable:

```sh
hostlier secret set MODEL_API_KEY --from-env MODEL_API_KEY --server production
```

`--stdin` is an alternative input mode; values must never be command arguments. Input is limited to 64 KiB. Do not echo credentials. Application YAML accepts flat literals, `$VARIABLE` for an exported local environment value, and `secret://NAME` for a stored controller reference, for example `secrets: {MODEL_API_KEY: $MODEL_API_KEY}`. Prefer environment sources for values that should stay outside the project file. Plain strings are now literal values; migrate old bare references to `secret://NAME`. `plan` and `deploy` synchronize declared values through the protected secret API before planning, while `validate` remains offline. Source values must never appear in plan output. Use `$$` for a literal leading dollar.

Use `hostlier secret delete NAME` only for the requested secret. The controller enforces retained-reference protection; do not work around rejection by editing its state files. Changing a secret revision and deploying a workload are separate operations; inspect the deployment plan before claiming an application is using the new value.

Run protected mutations serially and wait for each CLI command to confirm completion before starting the next. Approvals bind the reviewed controller generation; another completed mutation can make an outstanding review stale. Inspect secret/application/operation metadata before retrying an expired review, and never duplicate an already accepted operation.

A scoped CLI can create an absent secret when the owner explicitly approved its name for both use and creation. It cannot overwrite, delete, or read existing secrets. Obtain that permission through `hostlier reauth PROFILE`; the secret need not exist before pairing. A use-only deployment grant does not confer secret writes.
