---
name: hostlier-connect
description: Connect or renew a named Hostlier controller profile through one shared authentication workflow.
---

# Hostlier connect

Use `hostlier connect https://CONTROLLER` for a new controller (optionally name it with `--name PROFILE`) and `hostlier connect PROFILE` to renew a saved connection. Omitting PROFILE renews the selected server. Repeating the same URL and name renews in place. `reauth` and grouped `server connect` / `server reauth` spellings use the same workflow.

The default browser flow generates a local device signing key, opens the controller review page, and waits for explicit owner approval. Leaving all permission fields blank grants all access with no expiry or usage ceiling; selecting deployment restrictions requires exact apps, lifetime and deployment count, with optional named secrets. The owner must perform their own passkey prompt. Never request passkeys or one-time codes in chat. Older controllers may require a code entered in the initiating terminal. Initial owner setup and server-side administrator approval are documented in `docs/guides/controller-access.md`; these are distinct from routine laptop authentication.

Saved profiles remember their authentication method and email address where applicable. Renewals preserve the current server selection and existing credentials until successful issuance. A saved name cannot silently switch controller origins. New connections select their new profile; use `hostlier server use PROFILE` explicitly to switch to another existing profile. No disconnect or socket flag is needed for ordinary laptop renewal.

Advanced installations can explicitly select `--auth browser`, `--auth email --email OWNER`, `--auth request`, or `--auth token --token-stdin`. Legacy `--email`, `--request`, and `--token-stdin` remain accepted aliases for method selection. Do not combine conflicting methods or fall back after failed authentication. Email requires configured delivery and an allowed identity; it grants reads/preparation, not deployment approval. Administrator requests and tokens retain their legacy authority boundaries. Token renewals always require explicit `--token-stdin` and fresh protected input. Old profiles with no recorded method default to browser; select their intended method once.

Explicit creation permission permits a missing named secret to be created through the CLI after connection. It never allows reading or overwriting an existing secret. Use permission alone does not confer writes.

HTTPS certificate verification is mandatory. `HOSTLIER_CA_FILE` adds a trusted CA without disabling verification. Profiles and credentials live in owner-only files under `HOSTLIER_HOME` or `~/.hostlier`; never inspect or print token files. Inspect `hostlier server list` and use `hostlier status APP --server PROFILE` to verify scoped access. Controller-wide diagnostics may require broader authority. See `docs/guides/connect.md` for the canonical workflow.

For a controller with no owner, ordinary `connect` prompts for an SSH administrator destination. `--ssh USER@HOST` or an SSH config alias supplies it upfront. The laptop invokes only `hostlier-controller bootstrap` remotely, validates the returned origin and device binding, and opens owner enrollment followed by the pending scope review. The owner must create their own passkey; do not operate their authenticator for them. Root is optional; `--ssh-sudo` requests passwordless sudo explicitly. `--ssh-config FILE` and `--ssh-socket PATH` support nonstandard installations. Keep SSH host verification enabled, let the user verify and approve the native SSH fingerprint prompt for an unknown host (never auto-accept it), and never print the temporary enrollment URL into shared logs. No server-side hostlier CLI is required. Existing owner setups proceed to ordinary approval without minting another bootstrap link.

An SSH destination in `hostlier connect admin@HOST` installs a box under the selected master. Use `hostlier server use MASTER/BOX` to select it for workload commands. To move an already-installed box between masters, run `sudo hostlier-controller connect MASTER` on that box.
