# Controller hardening and fleet architecture

This is the implementation contract for the requested expansion. It is not a claim that every capability below is already implemented or live-verified.

## Agreed product behavior

- The CLI prints a request code. The owner reviews and signs the exact intent from their own terminal using an enrolled OpenSSH FIDO key with user verification and touch. Email enrolls a device; finite, hardware-signed deployment windows permit routine stateless deployments. See [setup and trust boundaries](email-device-auth.md).
- A central controller manages explicitly enrolled host agents. Each host retains local runtime ownership and durable execution receipts. Cross-host execution is an explicit placement decision, never automatic reassignment after an ambiguous failure.
- State management includes scheduled off-server encrypted backups, retention, import and verified restore, and separately approved database upgrades.

## Trust boundary

An API credential permits observation, planning and proposing authorized work. It does not mint approval, enroll a human credential, change approval policy, or directly mutate workloads. Legacy bearer credentials must not retain a bypass. The controller checks authorization before every protected handler, including lifecycle, secret changes, enrollment administration, domain changes, backup export and workflow reconciliation. Hardware signatures use a dedicated SSHSIG namespace, exact controller/request binding, required user-presence/user-verification flags and expiring single-use challenges.

Human public keys are enrolled and revoked through the protected local administration channel. Remote API credentials cannot request enrollment. The root-owned Unix socket remains an explicit, audited administrative recovery boundary: direct calls there retain local administrator authority. Normal CLI mutations use hardware approval even when connected through that socket. No bearer credential or request header selects local authority. The system cannot protect itself against an agent with root access to the controller, its runtime socket, its state files or the human's authenticator.

Approval covers actor, controller identity, target host identity, canonical request and current state generation. Deployment approval additionally binds the existing immutable plan, secret revisions, artifacts and package identities. Changes or expiration invalidate unconsumed approval. Before side effects, execution records durable consumption; an uncertain outcome is inspected rather than replayed. Terminal review responses contain sanitized intent; secrets and raw backup payloads are never rendered. The local dashboard displays a terminal approval command and cannot approve requests.

## Fleet boundary

Enrollment binds a host's public key and immutable host identity to the central controller. Host transport uses mutually authenticated TLS with revocation and exact leaf-certificate pinning. Approval and execution receipts bind host identity; an agent cannot substitute its address or route a request to another host. Each host independently validates the pinned controller identity, delegated actor and expected host generation before runtime changes. Disconnects preserve uncertainty rather than scheduling a second writer. Local volumes remain attached to their original host; migration requires an approved restore or transfer workflow.

The central service should not require a Docker socket or root runtime authority. Privileged host operations remain in the host agent. Stored credentials are separated by purpose; workload clients cannot obtain host or approver authority. Secret encryption must support independently supplied key material and documented recovery/rotation, without claiming that local encryption protects against a compromised privileged process.

## State boundary — implemented recovery workflows

Backup policy explicitly names source, destination, encryption configuration, schedule and retention. Creating or widening policy requires human approval; execution may repeat only within that approved scope. A failed or incomplete backup never becomes a successful recovery point, and retention must preserve the last verified recoverable copy. Destination credentials and encryption keys never appear in plans, logs or exported public metadata.

Controller metadata, database data and application volumes are separate recovery units. Import validates authenticated metadata, engine identity, payload size and digest before publishing a backup; the separate restore target validates native format and content. Restore creates an isolated target and verifies native database readiness and known recovery evidence before cutover. Live source/destination fencing prevents competing writers.

Database upgrades are explicit supported engine/version transitions with preflight, a verified backup, an isolated target, validation and separately reviewed cutover. Image replacement alone is not an upgrade. Failed preflight or verification leaves the source intact. Rollback after writes requires an engine-specific data compatibility procedure; the UI must not promise image rollback will undo data changes.

## Acceptance gates

1. The SSHSIG verifier rejects missing user verification or presence, wrong namespace/challenge/key, replay, expired requests, altered payloads and stale generation. Direct bearer calls to every protected route fail before side effects.
2. The packaged CLI produces an approval code, waits or resumes and observes only the approved request. Automated tests exercise real signatures from software test authenticators; real hardware compatibility still requires an operator smoke test.
3. At least two isolated host agents establish mutual authentication, execute the approved target only, reject replay/wrong-host commands, and retain uncertain operations across disconnect/restart.
4. An off-server backup is restored onto a fresh target and queried through the native database protocol. Interrupted upload, corrupt import, unavailable keys, retention failure and controller restart have explicit safe outcomes.
5. Each advertised database-upgrade transition is exercised with real source data, independent verification and a failed-upgrade rehearsal before being declared supported.

Signature verification uses golang.org/x/crypto/ssh and follows OpenSSH SSHSIG and U2F signature formats. The module requires Go 1.27.3 or newer.

S3 scheduling, authenticated encrypted bundles, retention, portable import, native restore verification and separately approved Docker database upgrades are implemented. The real-engine integration fixture restores encrypted S3 downloads through PostgreSQL 16→17, Redis 7.4→8.2 and Neo4j 5.26→2025.01, rejects corrupt native archives, and compares restored data. Controller tests cover upload/read-back failure, retained reservations, restart uncertainty, cutover authority and central key revocation across a real mutual-TLS host connection. See [database recovery](database-recovery.md) for configuration, supported data and limits. Kubernetes major-version upgrades fail closed until exclusive-writer fencing is implemented.
