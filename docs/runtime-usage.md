# Runtime resource usage

`hostlier usage APP` reads current workload resource consumption without requiring deployment approval or changing desired settings. It uses the authenticated application management API: `GET /v1/apps/APP/usage`.

The JSON response distinguishes measured `cpuCores` and `memoryBytes` from the approved project's `limits`. `cpuCapacityCores` and `memoryLimitBytes` come from the actual Docker container or Kubernetes deployment and describe the applied per-replica capacity. `desiredReplicas` comes from retained approved intent; `readyReplicas` comes from observed workload state.

`available: true` means a valid sample was collected. Genuine idle measurements remain numeric zero. `available: false` includes a `reason` and omits measurements; it does not imply zero consumption. Stopped Docker workloads and Kubernetes deployments with no ready replicas report unavailable measurements. `source` identifies `docker-stats` or `kubernetes-metrics`; `observedAt` identifies the collection timestamp when measurements are available.

Docker CPU cores are derived from workload and system counter deltas with the daemon's online CPU count. Memory bytes are Docker's measured memory usage. Both observations require an owned container. Effective limits are read independently from the daemon rather than inferred from desired project configuration.

Kubernetes measurements require the optional metrics API, normally provided by metrics-server. The observer verifies the application namespace, current deployment, and current-version pods before admitting metrics. It aggregates all measured containers in each admitted pod and excludes retained deployment versions and unrelated pods. Missing, malformed, or incomplete metrics produce an explicit unavailable response while preserving the desired settings and observed ready count. Usage inspection does not install metrics-server or mutate cluster resources.
