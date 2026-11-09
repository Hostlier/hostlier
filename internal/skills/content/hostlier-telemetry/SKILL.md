---
name: hostlier-telemetry
description: Render shared Hostlier collectors, manage scoped upload credentials locally, and query bounded metric and log history.
---

# Hostlier telemetry

Standard release installations automatically provision the minimal collector once per Docker host or Linux k3s node. `hostlier-controller init` stages bundled `collector-image/` assets and waits for readiness; the controller manages scoped credentials, authenticated image downloads, periodic reconciliation and repair. Check `telemetry.managed` in the status snapshot. No monitoring sidecar is added to each application. See `docs/telemetry-collectors.md` for prerequisites and automatic fleet mutual-TLS setup. Fleet agents use a dedicated collector-only mTLS listener, normally management port plus one; `HOSTLIER_COLLECTOR_URL` overrides its advertised origin.

For separately managed collectors, run `hostlier telemetry render --runtime docker|k3s --url https://CONTROLLER --output NEW_DIRECTORY` to produce a collector configuration and deployment manifest offline. The minimal image is the default; use `--image IMAGE` to select an image available to the target runtime; see `collector/README.md` for its reproducible build. The image must be available to every target runtime node. Rendering does not install the collector. Review the generated bundle and follow `docs/telemetry-collectors.md` for installation and certificate trust. Docker uses one collector per host; k3s uses one per node. Application sidecars may be arbitrary workloads; telemetry does not add a monitoring sidecar to each application.

Create upload credentials on the server through its protected socket: `hostlier telemetry token --name COLLECTOR --socket /run/hostlier/controller.sock`. This prints a secret once. Place it in the collector's protected environment or Kubernetes Secret without including it in source files, chat output, or logs. Credentials expire after 30 days and authorize metrics/log upload plus read-only collector image downloads; they cannot manage applications or read historical logs. List metadata with `telemetry collectors` and revoke an ID with `telemetry revoke ID`, using the same socket.

Read history with `hostlier telemetry metrics --app APP --server PROFILE` or `hostlier telemetry logs --app APP --component NAME --server PROFILE`. Optional `--revision ID`, `--limit N` (1–200), and `--before ID` narrow or paginate results. Use the returned `nextBefore` cursor. Logs may contain sensitive application data; do not copy their contents unnecessarily.

The current receiver accepts OTLP HTTP JSON gauges, sums and logs. Histogram samples are explicitly rejected; traces are not stored. Bounded history is diagnostic evidence, not deployment authority. A fresh collector status indicates delivery, not application health. Never invent missing observations or infer readiness from a missing sample.
