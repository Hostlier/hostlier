---
name: hostlier-ui
description: Start Hostlier's local deployment dashboard for shared runtime observations and hardware-key-approved lifecycle actions.
---

# Hostlier UI

Run `hostlier ui` to open locally before pairing, or `hostlier ui --server PROFILE` to select an existing controller. The foreground CLI prints a loopback URL and stays alive to serve the dashboard. `--port PORT` selects a fixed local port; otherwise an available port is chosen. Do not use `--host` or `--socket`; browser access never gains root Unix-socket authority.

The dashboard is hosted by the local CLI; the controller remains on the remote server. The Controller panel selects saved profiles or starts pairing with an HTTPS origin and profile name. The server administrator reviews the displayed code using `hostlier pairing approve CODE --socket /run/hostlier/controller.sock` in an interactive server terminal. Only successful enrollment saves a profile and starts observation. Existing terminal email/device enrollment profiles also work.

Open the complete printed URL, including its session fragment. Treat that URL as a local access capability. The controller credential stays in the CLI, while browser tabs share one upstream observation stream. Ctrl-C stops the dashboard and observation worker; accepted controller operations keep running.

Distinguish running processes, observed replica readiness, unavailable measurements and desired state. A running process is not proof of end-to-end application health. Stale or disconnected observations retain their capture timestamps and are marked stale; use the CLI and server evidence to investigate. Snapshots contain at most 128 applications and 128 operations and show truncation explicitly.

The UI is a monitoring console with CLI handoffs. Use Overview, Deployments, Resources, Hosts, Observability, Activity, Backups & Recovery, Access and Settings to inspect records and their relationships. Resource inventories show retained state; detail pages request fresh inspection. Unavailable endpoints must not be reported as empty inventories or healthy services.

Copy the relevant CLI command to your terminal, replace uppercase placeholders, inspect the plan and complete its human approval ceremony. Commands preserve the CLI executable, profile directory, selected controller and configured CA trust. They never contain provider/controller credentials or secret values, and the browser never executes them. Do not sign on behalf of a human, bypass approval, or infer deployment authority from observations. After an uncertain response, inspect the returned operation or approval ID before submitting another request.
