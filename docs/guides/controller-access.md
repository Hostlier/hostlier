# Controller access

A hardware security key is optional. Hostlier supports browser passkeys (including platform and synced passkeys), existing OpenSSH FIDO keys, and device-bound all-access credentials or deployment credentials with explicit limits.

For the common laptop setup and renewal workflow, see [Connect your laptop](connect.md). This page documents owner bootstrap, approval and permission boundaries.

## Connect a CLI

Run `hostlier connect https://hostlier.example.com --name laptop`. The CLI generates an Ed25519 signing key locally and opens the controller review URL in your default browser (and prints the URL, request ID and device fingerprint). Compare the fingerprint and review the access summary. Choose **All access** for full controller access with no expiry or usage limit, then approve with an enrolled passkey. This includes write access even when the connection is used for dashboard monitoring. Choose **Restricted deployments** to specify exact application names, lifetime and maximum deployments, with optional allowed secret names and permission to create missing named secrets. The shared approval page supports CLI, Minion and dashboard connections; it shows device identity, request expiry and the selected permission before verification. The CLI finishes automatically; no code copying is needed. If browser opening fails, open the printed URL yourself.

Every poll requires that CLI's private request secret and device signature. Polling cannot issue credentials before owner approval. Requests expire after ten minutes. Older CLIs/controllers can still use the single-use code, with five failed code attempts allowed. A successful redemption consumes the request. The CLI saves the token and signing key in its owner-only profile; subsequent requests require both. Restarting the controller invalidates pending pairing and browser ceremonies, but preserves enrolled passkeys and issued grants.

## Renew an existing profile

```sh
hostlier connect do-test
```

This uses the saved HTTPS controller URL and authentication method without requiring the old token. Browser approval is the default. The profile is replaced only after successful issuance; cancellation, expiry or rejection leaves the existing profile and credential intact. Renewing another profile does not change the selected server. Omit the name to renew the selected profile. `hostlier reauth NAME`, `hostlier server connect NAME`, and `hostlier server reauth NAME` are aliases for the same workflow. You do not need to disconnect or pass `--socket`.

Expired credentials stay in the local profile so the server URL and profile name remain available. Renewal requires fresh owner approval and does not automatically extend old authority. It issues a new device identity; earlier server credentials remain subject to their original expiry/revocation policy.

## Enroll the first passkey

Run the normal laptop command:

```sh
hostlier connect https://hostlier.example.com --name laptop
```

When the controller reports no owner, the CLI asks for your existing SSH administrator destination. You can supply it explicitly with `--ssh USER@HOST` or an SSH config alias. Add `--ssh-sudo` when the account needs passwordless sudo. The laptop invokes `hostlier-controller bootstrap` on the VPS; it never invokes or requires the `hostlier` CLI there.

The standalone command talks to the running controller through its protected local socket. It verifies that the requested HTTPS origin and pending device request match that controller before generating a ten-minute, single-use enrollment link. A public HTTPS caller cannot bootstrap an owner. If an owner is already enrolled, setup returns ready without generating another owner link.

The browser opens the returned link. Choose **Create passkey**, then review and approve the CLI connection on the next page. User verification, origin/RP checks and signed device proof remain mandatory; creating a passkey does not automatically approve a deployment scope. The enrollment URL contains a temporary administrator secret, so keep the terminal and link private. The controller stores public credential material, never the authenticator's private key.

For explicit local administration, the controller-only bootstrap command is:

```sh
hostlier-controller bootstrap --url https://hostlier.example.com --request PENDING_CLI_REQUEST_ID
```

It defaults to `/run/hostlier/controller.sock`; `--socket PATH` overrides a nonstandard local socket. The service must already be running. The old `hostlier security passkey --name Owner --socket PATH` remains available if the optional CLI is installed on the server, but normal first-time setup no longer needs it.

Existing hardware keys remain supported through `security keygen`, `security enroll` and `approve --key`. Browser operation review is available at `https://CONTROLLER/approve?request=ID`.

## Bootstrap a deployment without a browser passkey

A trusted server administrator can approve a pending CLI request through the protected local socket:

```sh
sudo hostlier pairing code REQUEST_ID \
  --app minion-stocks --secrets minion-stocks-engine-token \
  --create-secrets minion-stocks-engine-token \
  --for 2h --max-uses 20 --socket /run/hostlier/controller.sock
```

The waiting CLI completes automatically. With an older CLI, enter the returned code in the terminal that started the request. This is explicit server administration and is audited. The example grants restricted deployment access. To approve all access instead, omit all permission flags: `sudo hostlier pairing code REQUEST_ID --socket /run/hostlier/controller.sock`. An explicit `--socket` uses local administrator authority for other administrative commands as well. Remote bearer credentials never inherit that authority.

## Scope and revocation

An empty permission selection grants full remote administration: all applications, secrets and controller management APIs, no expiration and no usage ceiling. The stored grant explicitly has `scope: "all"`, `expiresAt: 0` and `maxUses: 0`. It remains device-bound and revocable; the token alone cannot authenticate without its signing key. Writes still use encrypted execution receipts and recheck authority before execution. Protected local owner bootstrap and recovery still require server administration.

All-access grants can create and update secrets without naming them during connection. Revoking the grant, device or approving passkey immediately prevents subsequent access and pending execution. Previously issued restricted credentials keep their original permissions; reconnect and approve a new selection to change access.

In the browser, selecting **Restricted deployments** requires application names, a valid lifetime and a deployment count, even if its fields are still blank. API and terminal callers select restricted access by supplying permission fields. A partial or invalid restricted selection never silently becomes all access. For restricted credentials, omitted secret names mean no secret access.

Deployment credentials permit stateless image deployments to 1–20 exact application names on this controller. They allow at most 20 named stored secrets, a lifetime of 1 minute–8 hours and 1–100 deployment approvals. Each approval reserves a use, even if it subsequently expires or fails. They cannot administer clients, read secret values, prepare workflows/dependencies, create persistent volumes, replace existing stateful apps, or fall through to unrestricted human approval. Their status/log reads are limited to named apps and their own operation receipts. Rollback and resource-scaling commands require a separate owner workflow.

Container images prepared by an installed project package can be uploaded by the paired laptop CLI before controller planning. Staging is isolated by authenticated client. There is no fixed image or aggregate archive byte cap; the controller preserves at least 256 MiB or 5% free space on the artifact filesystem.
Each upload request carries at most 1 MiB of image data within a 2 MiB HTTP body limit. Other remote API requests retain a 1 MiB HTTP body limit.
Commit verifies the archive, image manifest and configuration identities before
publishing shared content. Uploading does not run the image or consume a deployment
approval. The subsequent plan and execution still enforce the exact app, secret,
expiry, revocation and use limits. Repeated uploads and commit retries are supported.

For a restricted connection, to create a secret from your laptop, list its name in both **Allowed secret names** and **Secrets this CLI may create if missing** during approval. Then run `hostlier secret set NAME` and enter the value in the hidden prompt. Values travel over the saved HTTPS connection and are encrypted on the controller. Secret creation uses an actor-bound approval receipt; it cannot read, overwrite or delete existing secrets. Creation is limited to the explicitly approved names and grant lifetime and does not consume deployment uses. Use-only grants confer no write permission. Existing grants do not gain this capability automatically. Anyone allowed to deploy code that uses a named secret can make that code use the secret, so grant secret names deliberately.

Use `authorizations list`, `authorizations revoke ID`, or `clients revoke ID` with an all-access credential, owner approval or an explicit administration socket. `security revoke --fingerprint PASSKEY_OR_HARDWARE_ID --socket PATH` invalidates that approver's grants and pending approvals. Passkey IDs are returned at enrollment. Lost owner credentials can be recovered only through protected server administration. Use `hostlier connect PROFILE` for expired credentials or a different scope; approval is never automatic.

Legacy `connect --email`, `connect --request`, and `connect --token-stdin` remain available. They do not silently gain deployment authority from this change.
