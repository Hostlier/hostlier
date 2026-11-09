# Iteration 3 installed VM scaling gates

Updated 2026-10-29 11:39 UTC. Task-owned Lima fixtures only; retained application and database volumes.

- k3s prior 1 → 3 → 1, actual limits, routing through all three pods, approval guards, and Redis checks passed before interruption.
- k3s zero capacity survived full VM stop/start: actual desired/ready zero, HTTPS 503, start/restart rejected. Approved scale up restored actual desired/ready one and Redis `fixture=persisted`.
- Docker and k3s controller recovery passed: stop controller, metadata backup, move original `/var/lib/hostlier` aside, restore archive at exact original absolute path, start controller, authenticated HTTPS application/status, enabled services, protected logs, and existing Redis data verification.
- Recovery archives retained inside each VM at `/root/controller-scaling-recovery.tar`; original metadata retained at `/var/lib/hostlier-source-scaling-recovery`.
- Docker reboot exposed stock nginx competing for port 80 with installed Caddy. Disabled stock nginx and restarted Caddy/controller; HTTPS fixture recovered.
- Docker zero → full VM stop/start → zero remains stopped, actual zero and HTTPS503, start/restart rejected → approved up restoring app and Redis passed. The initial invalid-resource plan was a missing optional Redis helper; built and installed it with `hostlier package install redis`, then all gates passed.
- Docker vertical 0.25CPU/96Mi → 0.5CPU/128Mi passed against Docker inspect NanoCpus/Memory; replicas3 rejected, missing-TTY/forbidden bypass rejected, terminal decline and interrupted approval preserved one running container, `.hostlier` unchanged, Redis persisted. VM-local harness changed decline proposal to valid CPU0.4 because replica2 is unsupported before approval on Docker.
- Actual Docker pending-drain recovery passed with newly built fixed binary: restarted real retained predecessor `e584ced5f7fb`, set active record `previousRuntimeId` with controller stopped, started controller, then issued a fresh PTY-approved CPU0.4 scaling deployment. Verified predecessor is stopped, durable previous marker cleared, exactly one actual app container running, public HTTPS route healthy, and Redis persisted data intact. This recreates interrupted durable cleanup state; it does not inject a failing Docker stop API.

Both owned VMs stopped after verification; application/database fixtures, archives, and source metadata copies retained.
