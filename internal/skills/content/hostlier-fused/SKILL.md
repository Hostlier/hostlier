---
name: hostlier-fused
description: Connect a Hostlier controller to Fused Engine with OAuth and provision runtime-token secrets for Minion apps.
---

# Fused Engine connections

Run `hostlier fused connect NAME --engine-url https://ENGINE --server PROFILE`.
The controller registers a dynamic OAuth client and retains PKCE and client credentials.
The CLI prints the authorization URL and waits on a loopback callback. Open that URL,
sign in to Engine and approve the two displayed SDK/API token-management scopes.
Keep the CLI running until the browser reports success. When running the CLI through SSH,
forward its displayed callback port to your browser machine; a remote loopback address
otherwise refers to the wrong machine.

Use `hostlier fused status --server PROFILE` to inspect connection metadata.
Engine dynamic clients expire after one hour. Refresh does not extend client registration;
reconnect for later provisioning. An existing registered client can instead be selected
with `--client-id ID` and optional `--client-secret-ref SECRET`; register the exact HTTPS
callback printed by `fused status` and the same scopes. Never paste a client secret into flags.

Provision a dedicated Minion runtime credential into the controller's encrypted secret store:

```sh
hostlier fused token NAME --family-id SDK_FAMILY_UUID \
  --secret-name minion-engine-token --name UNIQUE_TOKEN_NAME \
  --allow OPERATION[,OPERATION] --server PROFILE
```

Use the exact Engine family UUID and canonical operation allowlist from its published SDK/API.
The Minion `fused.appId` remains the immutable runtime version UUID; these IDs differ.
Set its `fused.tokenSecret` to the returned secret name, then plan and deploy normally.
The container receives only the runtime token, never the controller OAuth credential.
An existing secret is not overwritten. If issuance fails ambiguously, inspect Engine's
named token list before retrying; no plaintext token can be recovered from it.

`hostlier fused refresh NAME` refreshes a live management grant.
`hostlier fused disconnect NAME` revokes that grant and removes its encrypted local state.
Disconnecting does not revoke already issued Minion runtime tokens. Manage those in Engine.
All mutations require owner approval or protected local `--socket` administration.
Scoped deployment credentials cannot create connections or issue Engine tokens.
