---
name: hostlier-agent
description: Run a Hostlier host agent pinned to its central controller.
---

# Hostlier agent

Run `hostlier-controller agent run --host ADDRESS --port PORT --controller-fingerprint SHA256` on an authorized host with its own persistent state directory and runtime. Obtain the central fingerprint through trusted administration. The host exposes a mutually authenticated TLS endpoint for that controller. Enroll the host certificate at the central controller before targeting `--on HOST`. Keep host state, runtime sockets and controller pin configuration inaccessible to workload agents; those are privileged trust boundaries.
