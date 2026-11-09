# Installed Docker and Nginx coexistence — 2026-10-30

Passed on a new local Ubuntu 24.04.5 LTS ARM64 VM with Docker 29.1.3 and
host-installed Nginx 1.24.0. Two manually managed Docker applications, their TLS
sites, and an external renewal timer were running before Hostlier initialization.
This was an actual systemd installation and CLI deployment, with no mocked runtime.

The rehearsal completed in **76.77 seconds**, making **1,148 fresh, verified HTTPS
requests** to the two original sites with **zero observed failures**. Each request
checked certificate trust, hostname, HTTP status, and expected application content.
Sampling does not establish an absolute guarantee of zero downtime.

| Check | Observed result |
| --- | --- |
| Default installation | Detected Docker and Nginx independently; selected `docker`, `nginx-existing`, and `dockerMode: existing` without runtime/proxy flags |
| Existing configuration | All 25 tracked config/content/service-definition entries retained identical hashes, permissions, and symlink targets |
| Existing applications | Both container IDs, start times, restart counts, configuration, and mounts remained unchanged; persistent content remained intact |
| Existing services | Nginx master PID 2701 and Docker PID 2742, process start times, enabled state, and service definitions remained unchanged |
| Existing certificates | Certificate/key file hashes, symlink targets, and served TLS fingerprints remained unchanged through deployment, restart, conflict rejection, and repeated init |
| New application | Actual installed CLI deployed an Nginx app at a new hostname; verified HTTPS served its expected content, including after restart |
| Hostname collision | A deployment under an existing site's hostname failed without creating a candidate container; both original sites remained unchanged |
| Repeated initialization | Preserved legacy sites and the newly deployed Hostlier route |
| External renewal | Original operator-owned service rotated both legacy certificates and reloaded Nginx; fresh trusted TLS fingerprints changed as expected and the new app still worked |
| App deletion | Deleting the Hostlier app preserved the original sites and renewed certificates |

The installer also waited for its managed collector to become ready. The test VM
was stopped afterward, then deleted at the user's request. Its guest snapshots
and CLI logs were deleted with the disk; this report preserves the observed
results. No product-code correction was needed for this run.

## Scope and reproduction

The checked-in [harness](../test/e2e/coexistence_vm.py) and
[VM instructions](../test/e2e/VM_TESTS.md#existing-docker-and-nginx-coexistence)
reproduce the test. `internal/cli/coexistence_vm_test.go` supplies an explicitly
opted-in software test authenticator through the production verification API.
The real installed CLI still performs interactive confirmation and waits for
independent signed approval.

Certificates came from a trusted private CA and were supplied as external
references. The renewal fixture performs real key/certificate rotation and Nginx
reload, but does not contact Let's Encrypt. Public ACME issuance/renewal, public
DNS, cloud networking, amd64 execution, and physical hardware-key interaction
were not tested by this rehearsal. The explicit k3s path was not run here.

The Python harness passed syntax compilation, the Go helper compiled for Linux
ARM64, and its ordinary non-opt-in test passed by skipping. CLI package quality
checks passed (54 files, 324 functions, maximum complexity 10, no missing comments).
The repository-wide quality check found unrelated existing complexity/comment
findings in controller, packageexec, and store files; this run does not claim that
the whole workspace passed that gate.
