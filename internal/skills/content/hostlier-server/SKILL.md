---
name: hostlier-server
description: Add a fleet controller, select server profiles, or open an SSH shell.
---

# Hostlier server

Use `hostlier server list` to inspect saved profiles and the selected box. Use `hostlier server list MASTER` to see a master’s enrolled boxes. `hostlier server use MASTER/BOX` selects the box for future workload commands. A box on the current master can be selected by name. `hostlier server use MASTER` selects the master itself.

Use `hostlier connect https://CONTROLLER` to create a laptop profile and `hostlier connect admin@BOX` to add and select a box over one SSH connection. `hostlier server add admin@BOX` enrolls it while keeping the current selection. The box connects outbound to the master on TCP 7464 using pinned mutual TLS and a persistent gRPC stream; only the master needs an inbound fleet firewall rule. A local release directory can be supplied with `--release-dir DIR`.

From an installed member, run `sudo hostlier-controller connect https://MASTER --name NAME`. On the master, run `sudo hostlier-controller approve CODE` using the displayed code and confirm the identity in the terminal. Both commands select the local administration socket automatically; no browser, CLI profile or manual fingerprint copying is needed. `hostlier server join HTTPS_MASTER` shares the same flow.


## SSH shell and commands

`hostlier server ssh` reuses the SSH destination and configuration saved by successful setup. `--server PROFILE` selects a controller; `--server MASTER/BOX` selects saved box SSH settings. The current selected box is honored, so commands never silently run on its master.

If a direct controller profile has no saved SSH connection, the CLI requests an approved managed account through the existing Hostlier login. `hostlier server ssh enable` explicitly provisions this account without opening a shell. This requires a controller release with managed SSH support and an existing OpenSSH daemon. The public key and Linux username are reviewed through Hostlier's ordinary approval protocol; restricted deployment credentials cannot create shell access.

The account has no sudo or Docker-group membership. Its home and authorized keys are controller-owned; `~/work` is writable. The private key remains in the local protected Hostlier directory. SSH verifies the host key obtained over the authenticated controller connection. Agent, X11 and port forwarding are disabled. This is an ordinary host user, not an application sandbox.

```sh
hostlier server ssh
hostlier server ssh -- uname -a
hostlier server ssh -- sh -lc 'df -h && free -m'
printf 'hello\n' | hostlier server ssh -- cat
hostlier server ssh --tty -- top
hostlier server ssh revoke
```

Place all remote arguments after `--`. Arguments are preserved literally; shell expansion and pipelines require an explicit `sh -lc`. Output is streamed without JSON wrapping; exit status matches SSH/the remote command. Revoking managed SSH access removes its login key, disables its shell and terminates its processes, while retaining workspace files. Revoking the associated Hostlier device also revokes managed SSH. SSH is a separately issued persistent credential; removing a local profile alone does not revoke it.

For an existing administrator connection, save its destination once:

```sh
hostlier server ssh configure --server default --ssh admin@server --ssh-config ~/.ssh/config
```

Omit `--ssh-config` to use standard OpenSSH configuration, keys and agent. Configuration does not install keys or grant privileges. Managed accounts do not receive administrator access: privileged Hostlier operations continue through separately approved controller maintenance commands.

A signed-in laptop can approve the same code with `hostlier server approve CODE --server MASTER_PROFILE`. The login requires `servers.add` permission or all access. Terminal confirmation is required; no browser opens. Grant bounded actions with `hostlier access authorize --device ID --on local --scope actions --actions servers.add --for 2h --max-uses 5`. App actions use exact `--app` names and the target `--on` host; deploy, rollback, scale, stop, delete/purge and sidecar removal have separate permissions. See the controller-access guide for the complete catalog and limits.
