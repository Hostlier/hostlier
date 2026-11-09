---
name: hostlier-doctor
description: Diagnose a Hostlier controller and runtime through its authenticated health report.
---

# Hostlier doctor

Run `hostlier doctor --server production` against the intended controller. If no profile is available, inspect `hostlier server list`; if connecting itself fails, use that transport or certificate error rather than claiming the doctor report ran.

Correlate the report with `hostlier status`, a specific application's status, controller logs and relevant operation IDs. Distinguish controller reachability, runtime availability and application readiness. A successful report does not certify public DNS, certificate renewal or every deployed endpoint.

Make fixes that address observed failures within the user's requested scope. Do not reinstall the server, switch its runtime or loosen certificate verification merely to clear a diagnostic failure.
