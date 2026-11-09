# Connect your laptop

[All guides](../../GUIDE.md)

Run `hostlier` on your laptop and `hostlier-controller` on the VPS. The CLI uses HTTPS; the controller's Unix socket is only for administration on the server itself.

## One connection command

Connect using the controller’s HTTPS URL.

```sh
hostlier connect https://hostlier.apps.example.com
```

The first unnamed profile is called `default`. Further unnamed connections to different controllers use names derived from their hostnames. Use `--name production` for a chosen profile name. An SSH destination such as `hostlier connect admin@box.example.com` adds a box to the selected master. [Fleet setup](install.md#add-a-box-to-a-master-controller)

The default flow opens a browser review page. Compare the device fingerprint and approve with your enrolled owner passkey. Leave every permission field blank for all access, no expiry and unlimited uses. To restrict access, enter application names, lifetime and maximum deployments, plus any allowed secret names. The CLI completes automatically and selects the new profile. For restricted connections, if a secret does not exist yet, explicitly allow its name for creation as well as use. All-access connections can create and update secrets without preselecting names. Older controllers may still ask for a one-time code.

If the controller has no owner yet, this same command asks for your SSH destination (`USER@HOST` or a saved SSH config alias). The CLI uses your existing SSH credentials to run the standalone controller's bootstrap command, then opens a one-time enrollment link. Create your owner passkey; the browser continues to the pending CLI scope review. No `hostlier` CLI installation is required on the VPS.

You can supply the SSH destination upfront:

```sh
hostlier connect https://hostlier.apps.example.com --name production --ssh admin@vps --ssh-sudo
```

`root` is not required. The account needs access to the controller's protected administration socket; use explicit `--ssh-sudo` for passwordless sudo. SSH aliases can specify user, identity, port and jump host in your normal SSH config. Use `--ssh-config FILE` for a different config and `--ssh-socket PATH` only for a nonstandard remote socket. Both controller and laptop CLI must include SSH bootstrap support. The running service must be reachable by HTTPS before setup.

SSH host verification remains enabled. For a new SSH host, the native SSH client displays its fingerprint and asks you to confirm it. Verify the fingerprint through your hosting provider or another trusted channel before accepting. Hostlier never answers this prompt for you, and a changed host key is still rejected. No separate SSH login is needed just to establish host trust. SSH agent forwarding is disabled. The CLI validates the returned enrollment link against the requested HTTPS origin and device request. SSH options are not stored in your profile and are not needed for later connections once an owner exists.

See [initial owner setup](controller-access.md#enroll-the-first-passkey) for the trust boundary. Merely opening the public controller page does not grant access.

Renew the same connection when its credential expires or its scope needs changing:

```sh
hostlier connect production
```

Omit the name to renew the selected server. Repeating the original URL with the same `--name` also renews that profile. `hostlier reauth production` and the `hostlier server connect` / `hostlier server reauth` spellings are compatibility aliases for the same workflow.

Renewal uses the saved URL and authentication method without needing the old token. It keeps the existing profile until fresh authentication succeeds. A named renewal preserves the currently selected server. An existing profile cannot be silently redirected to a different controller URL; choose a different profile name for another controller.

## Select a connection for commands

```sh
hostlier server list              # saved profiles and the current selection
hostlier server list production   # boxes enrolled with this master
hostlier server use production/box
hostlier status                   # workload status on box
hostlier server use production    # back to the master itself
```

`server use box` selects an enrolled box on the current master. A saved profile name takes precedence over a box of the same name. Use `MASTER/BOX` to make the destination explicit. Selection persists between commands without authenticating again. Fleet and access administration remain on the master. For one command, `--on BOX` overrides the workload target, and `--server PROFILE` targets that master directly. An explicit administration socket uses its own local controller.

HTTPS certificate verification is mandatory. `HOSTLIER_CA_FILE` adds a trusted private/test CA without disabling verification. Profiles and credentials use owner-only files under `~/.hostlier`, or `HOSTLIER_HOME`. `hostlier disconnect NAME` removes a local profile; it does not revoke the server credential or stop applications. Do not disconnect to renew an expired credential.

## Advanced authentication compatibility

Browser approval is the default. Installations already using another method use the same `connect` command and can select it explicitly:

| Method | Options | Requirements |
|---|---|---|
| Browser | `--auth browser` | An owner passkey or administrator approval of the pending connection request |
| Email | `--auth email --email owner@example.com` | Configured email delivery and an allowed address; email enrollment alone does not grant deployment authority |
| Administrator request | `--auth request` | A trusted administrator approves the legacy enrollment request |
| One-time token | `--auth token --token-stdin` | A fresh administrator-issued token supplied through protected stdin |

The method and email identity are remembered after successful connection. For token-based renewal, explicitly supply `--token-stdin` again with a fresh token. Tokens, email codes and private keys are never stored as authentication preferences. Profiles saved by older CLI versions have no recorded method and keep the browser default; select the intended method once when renewing them.

Legacy `--email`, `--request`, and `--token-stdin` spellings remain supported. They are inputs to the same connection workflow, not additional connection commands. Conflicting methods are rejected before contacting a controller, and authentication failure never falls back to a different method. To switch methods, specify `--auth METHOD`; the saved method changes only after successful authentication. These methods retain their existing authority boundaries; consolidating the CLI does not broaden access.

For administrator setup, scope enforcement and revocation, see [Controller access](controller-access.md). Next: [Deploy an application](deploy.md).

The [fresh-VM fleet test](../../test/e2e/VM_TESTS.md#fleet-handoff-on-fresh-vms) exercises SSH installation, laptop selection, and moving an installed box between masters.
