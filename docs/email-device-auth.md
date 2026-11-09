# Email enrollment and optional hardware-key authorization

For the default controller URL → browser approval → automatic CLI completion flow, browser passkeys, and scoped deployment credentials, see [Controller access](guides/controller-access.md). Hardware keys are optional; this guide documents the retained email and OpenSSH FIDO paths.

Hostlier sends Resend and Plunk email through the `hostlier-email-enrollment` Unified App on `https://eng.usefused.com`. Provider API keys stay in the Engine’s credential bucket; the controller holds only a scoped app execution token. Native SMTP remains available. Email registers a revocable device for reads and preparation. It does not grant deployment or administrator authority.

## Configure enrollment once

Create an owner-only file on the controller, for example `/etc/hostlier/auth.yaml`:

```yaml
allowedEmails:
  - owner@example.com
provider: resend
from: hostlier@example.com
fused:
  engineURL: https://eng.usefused.com
  appFamilyID: 1b1a3e00-54c3-458f-92c2-d9522d083eab
  tokenFile: /etc/hostlier/email-execution-token
```

Use `provider: plunk` to select Plunk through the same hosted app. The Unified App selects exactly one provider operation and renders the fixed device-enrollment template. The sender must be verified for that provider in the Engine's selected bucket. `fused.tokenEnv: HOSTLIER_EMAIL_EXECUTION_TOKEN` is an alternative to `tokenFile`; do not specify both. `engineURL` and `appFamilyID` default to the values shown above. The Engine executes the dedicated app family’s active version, initially `1.0.0`. Promoting a new version is an explicit Engine administration action. Custom Engines require an explicit HTTPS origin and that Engine's own app family UUID and execution token.

The source and deployment config live at [hostlier-email.ts](../../.fused/unified_app/hostlier-email.ts) and [hostlier-email.yaml](../../.fused/unified_app/hostlier-email.yaml). Only the Resend `postEmails` and Plunk `sendEmail` operations are selected. Existing provider credentials remain in the Engine's `default` bucket and are never exported to Hostlier. The deployment receipt and execution token are retained under the ignored, owner-only `.hosting/hostlier-email/` directory for this workspace. Provision only that scoped execution token to each authorized controller; never provision the Fused management login or a provider API key there.

When migrating older controller configuration, replace `apiKeyFile`, `apiKeyEnv` and `endpoint` with the `fused` block. Those old fields are rejected by strict decoding so provider API keys cannot accidentally be interpreted as Engine execution tokens. SMTP configuration does not change.

The controller performs one bounded execution request, using the same random delivery identity for Engine and provider idempotency. It requires `status: succeeded` plus the app's positive `sent`, matching provider and message ID receipt. Failed, queued, redirected, oversized or ambiguous responses invalidate the pending code without automatic retry or provider fallback. Provider acceptance does not prove inbox delivery. Email addresses, codes and device context are processed by the selected Engine; code verification, expiry, allowlists, rate limits and device signatures remain on the controller.

For native SMTP:

```yaml
allowedEmails:
  - owner@example.com
provider: smtp
from: hostlier@example.com
smtp:
  host: smtp.example.com
  port: "587"
  mode: starttls
  username: hostlier
  passwordFile: /etc/hostlier/smtp-password
```

`mode: tls` supports implicit TLS, normally on port 465. STARTTLS is mandatory when selected; neither delivery path falls back to plaintext. Certificate verification uses system trust. Use `passwordEnv` instead of `passwordFile` if preferred. Configuration and secret files must be regular owner-only files (`chmod 600`). Start the service with `hostlier-controller run --auth-config /etc/hostlier/auth.yaml`, or set `HOSTLIER_AUTH_CONFIG` in its service environment. Operator configuration is loaded at startup; changes require restart.

Adding an address to this exact allowlist is an administrator decision. Mailbox ownership is verified during enrollment. Removing an address or disabling email enrollment invalidates device identities enrolled through that policy. Remote API credentials cannot edit the file or change the allowlist. Protect access to the mailbox; email is not phishing-resistant authentication.

## Connect from your own terminal

```sh
hostlier connect https://controller.example.com --auth email --email owner@example.com --name laptop
```

Renew this profile later with `hostlier connect laptop`; its email authentication method and address are remembered.

Enter the emailed code in that terminal. Hostlier generates an Ed25519 device key, binds the code to its public key and verifies possession of its private key. The email shows the controller and device fingerprint. Codes expire after five minutes, allow five attempts and work once. Requests have global rate limits, per-recipient cooldowns and hourly delivery limits. Unknown/disallowed addresses and failed delivery produce the same receipt; timing is not claimed to hide account membership. Failed or ambiguous provider responses invalidate the code and are not automatically retried.

Device credentials use the existing owner-only credential store. Ordinary requests and live status streams are signed over method, URL, body hash, timestamp, nonce and the controller's current process epoch, in addition to the bearer credential. Stolen tokens alone cannot authenticate device-bound profiles. Server restarts invalidate old request signatures; the CLI obtains the new epoch automatically without another email. Revoke a device with `clients revoke ID`; removing a local profile with `disconnect` does not revoke its server-side access.

## Enroll independent approval authority

On your trusted computer, with a FIDO2 key supporting PIN verification attached:

```sh
hostlier security keygen --key ~/.ssh/hostlier_approval
```

This invokes FIDO-capable OpenSSH with `verify-required` and touch enabled. It stores a hardware key handle, not the hardware's signing secret. PIN entry stays local. For initial bootstrap, copy only the `.pub` file to the controller through your existing trusted administration channel and run:

```sh
hostlier security enroll --name Owner --public-key /path/to/hostlier_approval.pub --socket /run/hostlier/controller.sock
```

Enroll a backup hardware key the same way. Public-key enrollment trusts the administrator to enroll a real authenticator; this implementation does not validate manufacturer attestation. Controller verification accepts only OpenSSH FIDO public-key types and cryptographically signed user-presence and user-verification flags. A key with zero counters is supported; nonzero counters must advance. Signatures are domain-separated and bind controller identity and configured hostname, exact intent fingerprint, decision, key, generation, nonce and expiry. Challenges expire after two minutes and are consumed even on failure.

Root access, the controller state directory, runtime sockets and the local administration socket are trusted recovery authority. Do not give coding agents access to them or to your human authenticator/signing session. Routine approval does not require SSH to the controller.

## Authorize a bounded deployment window

```sh
hostlier clients list
hostlier authorize --device DEVICE_ID --on local --app api,worker --scope deploy --for 2h --max-uses 20 --key ~/.ssh/hostlier_approval
```

`--on HOST_NAME` targets an enrolled host instead of the controller's local runtime. Add `--secrets api-token,worker-token` only for existing secret references those applications may receive. The owner reviews the exact scope and signs from their terminal. A window is tied to one device, exact host identity, named apps, named secrets, approving key, expiry and use budget. Limits are one minute to eight hours, 1–20 apps, at most 20 secret names and 1–100 deployments. The agent can deploy arbitrary code within that deliberately granted scope, so grant secret access accordingly.

During the window, matching deployments run without another hardware interaction or a redundant CLI confirmation. The controller still validates each exact plan, state generation and request identity. It reserves a use when automatically approving a proposal; abandoned or failed attempts can consume uses.

Database operations, persistent-volume changes, existing stateful source apps, provider workflows, dependency provisioning, secret writes/exports, host enrollment, client management and new authorizations remain outside deployment windows. They require fresh exact-request approval. The initial window supports stateless application deployment only; it is not general-purpose automation authority.

### Sandbox windows for agents

An exact-name window needs a new signature for every new app name. A sandbox window instead delegates a name prefix, so an agent can create, update and remove its own test apps without asking you again:

```sh
hostlier authorize --device DEVICE_ID --on local --app-prefix try- --max-apps 5 \
  --route-domain try.apps.example.com --scope deploy --for 8h --max-uses 100 --key ~/.ssh/hostlier_approval
```

- **Names.** The prefix must be lowercase and end with a hyphen. The agent can deploy any new `try-*` name. It can update only `try-*` apps that one of this device's sandbox windows created. An existing app with a matching name that the sandbox did not create stays owner-only.
- **Capacity.** `--max-apps` (1–20, CLI default 5) caps how many of the window's apps can exist at once. Deleted apps free their slot.
- **Routes.** With `--route-domain`, every public route must be that domain or one of its subdomains. Without it, ordinary controller domain and route-ownership checks still apply.
- **Lifecycle.** The device can stop, start, restart and delete (never purge) apps its sandbox created, and can read their status, logs and operations.
- **Teardown.** When the window ends, through expiry, revocation, device revocation or key revocation, the controller queues an ordinary delete for each app the window created within about a minute. Deletion keeps the usual retained record; purge stays with you. Add `--keep-apps` to leave them running instead.
- **Scope.** Sandbox windows are local-runtime only for now. They share the stateless limits of every window: no volumes, dependencies, workflows or secret writes. Use counts work as for exact windows.
- **Restricted connections.** A device connected with deployment restrictions keeps its own grant. A live sandbox window authorized for the same device widens it only to the sandbox's own apps.

For requests outside a window, the agent prints a code. In your own terminal:

```sh
hostlier approve CODE --key ~/.ssh/hostlier_approval --url https://controller.example.com
```

Use the configured controller hostname for signing; alternate DNS aliases and IP URLs are rejected when they do not match the signed hostname. Review the retained intent, confirm, then enter the key's PIN and touch it. The originating CLI waits and executes only that stored request. If interrupted, use `hostlier approval REQUEST_ID --watch` with the original device profile. Codes identify requests; they cannot authorize them.

Use `authorizations list` / `authorizations revoke ID` to inspect or revoke a window. Device revocation, key revocation, host enrollment revocation and expiry also invalidate its outstanding authority. A lost key can be revoked through local recovery with `security revoke --fingerprint SHA256:... --socket PATH`. Email alone cannot replace an approval key. Completed execution receipts support idempotent result retrieval; uncertain interrupted executions require investigation rather than an automatic second mutation.

## Verification status

Automated tests use genuine cryptographic signatures from test-only software authenticators, signed-device requests, simulated HTTPS email providers and separate mutually authenticated host listeners. They do not prove compatibility with a particular physical key, OpenSSH installation, SMTP account or live provider configuration. No real enrollment email has been sent as part of these tests. SMTP/provider acceptance is not proof of inbox delivery.
