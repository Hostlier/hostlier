---
name: hostlier-validate
description: Validate a Hostlier project file and local deployment artifacts without contacting a controller.
---

# Hostlier validate

Use `hostlier validate --file .hostlier` before planning a deployment. The default file is `.hostlier` in the working directory. This command checks project structure and inspects local artifacts without executing their code or requiring a saved server connection.

Fix reported configuration errors against the actual application's ports, probes and runtime. Version 1 uses `image`, `port` and `route`. Version 2 uses `source`, `workload` and singular `endpoint` or named `endpoints`; private services and workers require an explicit `health.exec` supported by the actual image. For package inputs, install the adapter and run `hostlier plan` in the project directory.

Successful validation does not prove remote capacity, DNS, certificates, image availability or application readiness. Use `hostlier plan --file .hostlier --server production` for controller planning when requested.

For a complete offline version 2 OCI image example including HTTP/gRPC endpoints, managed files, encrypted environment references, database dependencies and sidecars, run `hostlier skills show deploy`. These commands need no source checkout or controller.

Traffic policies must name explicit gRPC services/methods or HTTP exact paths/methods. They require public endpoints, preserved paths, supported streaming transports and a compatible edge. Validation rejects empty allowlists, wildcards, incompatible HTTP versions, and sibling endpoints shadowing explicit policy paths. Controller planning also rejects paths owned by active or pending deployments and reports the owning app, endpoint and URL. See `hostlier skills show deploy` for the complete `grpc.services` and `http.routes` schema.
