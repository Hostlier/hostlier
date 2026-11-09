# Publish a local app through Fused Bridge

Hostlier assigns a stable HTTPS hostname. Bridge opens a dedicated outbound TLS
WebSocket connection and forwards requests to the app's loopback HTTP port.
Engine pairing, operation calls, and event delivery are independent of this connection.

```text
Visitor → Hostlier HTTPS edge → controller's tunnel broker
                                      ↕ outbound TLS connection
                                Fused Bridge → localhost:3000
```

## Configure the Hostlier gateway once

Add these flags to the existing controller service command, retaining its other options:

```sh
hostlier-controller run \
  --tunnel-domain tunnels.example.com \
  --tunnel-url https://controller.example.com
```

`--tunnel-domain` must be a dedicated DNS suffix, outside the controller hostname.
It reserves the whole namespace against ordinary Hostlier deployments. Existing
app routes under that suffix cause startup to fail. `--tunnel-url` is the externally
reachable HTTPS controller origin, without a path or query.

Point `*.tunnels.example.com` at the gateway. With Hostlier's owned Caddy edge,
approval of each tunnel now adds its exact public hostname to Caddy and waits for
a certificate-valid TLS handshake before returning the lease. Existing live leases
are reconciled when the controller restarts. Hostlier's Caddy service reload uses
its saved live configuration, so tunnel routes survive a reload. The controller
hostname route remains available for management and
`/v1/tunnels/NAME/connect` traffic.

For an externally managed edge, configure HTTPS ingress for the suffix yourself.
It must preserve the original Host header, allow WebSocket upgrades, and forward
to the controller's **remote TCP listener** (by default `127.0.0.1:7463`). Never
forward public traffic to the privileged administration Unix socket.

For an independently managed Caddy edge using an already provisioned wildcard
certificate, the additional site block is:

```caddyfile
*.tunnels.example.com {
    tls /etc/ssl/tunnels/fullchain.pem /etc/ssl/tunnels/privkey.pem
    reverse_proxy 127.0.0.1:7463 {
        flush_interval -1
    }
}
```

Integrate this into the edge's actual configuration source; do not replace existing
Hostlier-managed routes. An Nginx edge likewise needs HTTP/1.1 upstream forwarding,
Host preservation, Upgrade/Connection handling, disabled proxy buffering, and
sufficient read/send timeouts for long streams. Certificate provisioning, wildcard
DNS, and ingress installation are operator setup in this version; the CLI does not
purchase a domain or modify DNS. Hostlier allocates names beneath the configured suffix.

## Let Bridge register and forward the app

Run one command on the machine hosting the app:

```sh
fused-bridge tunnel 3000 --hostlier https://controller.example.com
```

Bridge uses the same browser/passkey connection flow as `hostlier connect`.
It opens the controller's review page, waits for owner approval, and privately
saves a device-bound credential. Choose all access for tunnel management;
app-deployment-only credentials cannot manage tunnels. No email enrollment is
required. Use `--no-browser` to open the printed approval link elsewhere.

For a controller with no owner, Bridge prompts for an SSH administrator destination
and runs `hostlier-controller bootstrap` on the server. The VPS needs only the
controller binary. Native SSH host-key verification remains enabled. Optional
`--ssh`, `--ssh-config`, `--ssh-socket` and `--ssh-sudo` customize that setup.

Bridge then generates a stable tunnel name and connector identity, requests
publication through the normal approval API, and starts forwarding once authorized.
Explicit `--email ADDRESS` or `--auth email` retains email enrollment for operators
who enable it; `--auth browser` selects browser approval. Methods never silently
fall back to one another.

The controller and login are remembered, so later runs can use
`fused-bridge tunnel 3000`. Use `--name demo` for a chosen hostname; repeat that
option when reconnecting. A missing controller URL is prompted for on first use.
`--config`, `--bridge-id`, `--for` and `--connections` remain optional overrides.
No Hostlier CLI subprocess is used.

Existing bearer or `device-v1:` management credentials in `HOSTLIER_TOKEN` or
`--token-env NAME` remain supported for automation. They are optional and are not
saved by Bridge. App-deployment-only credentials cannot manage tunnels. Hostlier's
independent owner approval remains required and is separate from sign-in.

**The local address stays in Bridge.** Registration contains only `name`,
`bridgeId`, `connections`, `durationSeconds`, and `tokenVerifier`. The controller
rejects a `target` registration field and returns only public lease metadata.
Hostlier does not save the local target or generate Bridge configuration files.
Approval shows the public URL, connector label, lifetime and connection count;
the owner is authorizing that connector to publish traffic from a service it selects.

Bridge keeps credentials under its platform user config directory in
`fused-bridge/hostlier`, with a private 0700 directory and 0600 files. Use
`FUSED_BRIDGE_HOSTLIER_HOME` to override it. Management credentials and signing keys
are stored per controller, separately from the tunnel config. Bridge prints the
config path. An already configured tunnel needs no management credential.
`--ca-file FILE` adds a private CA and is remembered; TLS verification stays enabled.

Advanced setup-only and runtime-only commands remain available. To run a saved
connector directly: `fused-bridge tunnel run --config FILE`. To explicitly enroll
a new device or renew in place: `fused-bridge tunnel login --hostlier HTTPS_ORIGIN`.
No disconnect is needed; failed or cancelled renewal preserves the saved credential. A new device
identity does not inherit the previous device's reservations or pending approvals.

Repeat the short command to resume setup and begin forwarding after cancellation
or response loss. For explicit management, use the config path printed by Bridge:

```sh
fused-bridge tunnel setup --config bridge-tunnel.json
fused-bridge tunnel status --config bridge-tunnel.json
fused-bridge tunnel revoke --config bridge-tunnel.json
```

Management commands use the same existing credential identity. `--token-env NAME`
selects another environment variable. Bridge preserves its generated credential
and approval identity across retries. For denied or expired approvals, inspect the
result and explicitly request another review with `--new-approval`. Changing the
saved local target does not require changing Hostlier's registration, but Bridge
must be restarted to use the new local configuration.

Run Bridge in the app's network namespace, or publish its HTTP port to host loopback.
The target must use `localhost` or a literal loopback address and an explicit port.
Bridge validates that constraint locally. `bridgeId` is an operator-selected label,
not cryptographic proof of an Engine pairing. The public gateway cannot attest
which local process the connector credential holder selects.

## Lease lifecycle and generic administration

Status is `connected`, `offline`, `expired`, or `revoked`. A connected status means
there is a reverse transport, not that every application route is healthy.
Public traffic to an offline lease returns 503; unknown, expired and revoked
hostnames return 404. App hostnames never dispatch management APIs.

Reservations and verifiers survive controller restarts. Bridge reconnects without
replaying interrupted requests. Cookies and Authorization pass through, while the
broker rebuilds forwarding headers. Client IP is the immediate upstream peer,
not a trusted visitor IP supplied by the edge.

Revocation closes active streams. New requests fail immediately on expiry; the
expiry worker closes existing streams within its 250 ms interval. The default
lifetime is 24 hours, bounded to 1 minute–720 hours. The default four-connection
pool can be changed to 1–16 at setup. Long-lived streams occupy pool capacity.
There are at most 1,000 retained names.

There is no automatic renewal. To rotate or extend, revoke and recreate with a new
Bridge config path and restart the connector. Names remain reserved for their
owner; protected local administration can reassign a revoked name. Identical
registration retries recover the original lease without extending expiry.

Hostlier retains generic administration commands for operators and other clients:

```sh
hostlier tunnel list
hostlier tunnel status demo
hostlier tunnel revoke demo
hostlier tunnel create demo --bridge-id laptop --from-env CONNECTOR_TOKEN --for 24h
```

Generic creation returns public registration metadata; the caller retains its
chosen token. The old `--target` / `--output` handoff-generation options are rejected
with guidance to use Bridge setup. Existing handoff files remain runnable in Bridge.

Disable publication by removing both gateway flags and restarting. Reservations
remain stored. Revoke existing leases before changing the gateway domain or origin.

## Protocol and deployment scope

Supported: HTTP/1 upstream requests, streaming request/response bodies, SSE and
ordinary application WebSocket upgrades. The outer transport is the versioned
`hostlier-http-v1` binary WebSocket protocol over verified TLS. No raw TCP/UDP,
native HTTP/2 gRPC, or extended CONNECT support is claimed for this tunnel.

The first implementation uses one controller process and its durable store;
multiple independent gateway replicas do not share reverse connections. A load
balancer must route public traffic and connector connections to the same gateway.
TLS is terminated by the public edge, so this is not end-to-end TLS to the local app.

## Local verification

Build Bridge from its repository, then run from `hostlier/`:

```sh
HOSTLIER_TEST_BRIDGE_BINARY=/absolute/path/to/fused-bridge \
  go test -race ./internal/controller -run 'Test(Tunnel|BridgeBinary|BridgeHTTPSetup)' -count=1
go test ./internal/cli ./internal/controllercmd ./internal/tunnel
```

The integration uses temporary local TLS servers and a trusted test CA. It does
not require Docker, real DNS, or a live public deployment.
