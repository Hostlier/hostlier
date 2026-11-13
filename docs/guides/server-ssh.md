# Server SSH

Use the selected Hostlier server for an interactive Linux shell or a command:

```sh
hostlier server ssh
hostlier server ssh -- uname -a
hostlier server ssh -- sh -lc 'df -h && free -m'
hostlier server ssh --server production --tty -- top
```

Arguments after `--` belong to the remote command, including `--help` and other flags. Each argument is quoted literally for the remote shell; use `sh -lc` explicitly for pipelines or expansion. Input, output and errors stream directly, and the CLI preserves the command's exit status. OpenSSH handles terminal resizing and interactive terminal behavior.

## Reuse setup access

Successful `hostlier connect URL --ssh USER@HOST` setup and `hostlier connect USER@HOST` / `hostlier server add USER@HOST` retain their SSH destination and configuration. A selected fleet box uses its own saved destination, never its master's. `--server MASTER/BOX` selects that saved connection explicitly.

For older profiles, you can record an existing SSH connection once:

```sh
hostlier server ssh configure --server default --ssh admin@server
# Or use an existing SSH alias, identity and port from a configuration file:
hostlier server ssh configure --ssh production --ssh-config ~/.ssh/config
```

A one-off `--ssh USER@HOST` override does not replace the saved connection. Passwords and private keys are never copied into Hostlier profile metadata. SSH configuration, keys and the local SSH agent work as they do during setup. Host-key checking remains enabled; agent, X11 and port forwarding are disabled.

## Automatic unprivileged access

When a direct controller profile has no saved SSH connection, `hostlier server ssh` requests managed access through the existing Hostlier login. You can provision without opening a shell:

```sh
hostlier server ssh enable --server default
```

Both CLI and controller must include this feature. Older controllers return an upgrade-required message. The server must already run a compatible OpenSSH daemon; Hostlier does not install SSH, change firewall rules, or weaken its authentication settings. The SSH port must be reachable at the saved controller hostname. Fleet boxes need their saved setup connection or a direct controller profile.

The CLI generates an Ed25519 private key in its protected local state directory and submits only the public key. The controller reviews the key fingerprint and account privileges through Hostlier's existing approval protocol. Restricted application credentials cannot provision users. A live all-access authorization can approve the exact request under the existing policy.

The controller creates a dedicated Linux user for this device, with a locked password, no sudo access and only its own group. It does not join the Docker group. Its home and `.ssh` directory remain root-owned; `~/work` is writable. Authorized keys disable forwarding and user startup scripts. This is a normal host account, not an application sandbox: ordinary world-readable host files and outbound networking remain accessible.

The authenticated controller response pins the server's SSH host key before the first connection. The private key stays on the CLI machine. Existing administrator SSH credentials remain separate; configuring an administrator connection retains that account's existing privileges.

## Revocation and maintenance

```sh
hostlier server ssh revoke --server default
```

Revocation removes the managed authorized key, disables its shell and terminates the user's processes. Workspace files are retained. Revoking its Hostlier device also revokes managed SSH access. SSH is a separately issued persistent credential; deleting the local profile or ending a deployment authorization is not SSH revocation.

The managed account receives no blanket sudo rule. Privileged Hostlier work continues through separately approved [controller maintenance](../controller-maintenance.md). This change does not add remote migration execution: Docker-to-k3s apply and prerequisite changes still use the documented root-only migration workflow. An unprivileged SSH shell cannot run those as root.

## Verification

Focused Go tests cover argument preservation, raw streams and exit codes, profile/box selection, protected local keys, authenticated host-key pinning and approval enforcement. `python3 test/e2e/ssh/run.py` runs actual user creation, SSH login, privilege checks, idempotent provisioning and revocation in a disposable Ubuntu container with no host mounts or published ports. It also checks that revocation retains workspace data.
