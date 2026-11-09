# Automatic cleanup

The controller checks for abandoned artifacts at startup and every minute. Image uploads that have been idle for one hour are eligible for removal. Appending a chunk or replaying a verified chunk renews the upload lease, so interrupted uploads can resume within that window.

Unfinished package binary uploads expire after 24 hours of inactivity, even if no further uploads arrive. Successful chunk retries and verified installation previews renew that lease. Installed package executables are stored separately and are never removed by upload cleanup. Staging cleanup also runs when the runtime does not support image-cache eviction.

Committed artifacts also receive a one-hour grace period. After that, their receipts, archive files, and exact Hostlier image-cache entries can be removed when no application, retained revision, unexpired deployment plan, or unfinished operation references them. Sidecar images are included. Shared archives remain while any retained receipt needs them. Failed and completed operation history alone does not retain artifacts indefinitely.

Docker deployment rollback immediately attempts to remove failed candidate containers, their network anchor, and an empty application network. Cleanup uses its own deadline even if the deployment timed out. A durable cleanup journal records the exact resources before rollback, allowing the controller to retry after daemon failures or a restart. Active or retained containers are protected; ownership labels are checked before deleting containers or networks. Networks with connected consumers are preserved.

Image-cache eviction retries until it succeeds. Docker eviction never forces removal of an in-use image. Artifact expiry also supports the K3s image cache; the failed workload journal described above is specific to Docker.

Successfully uploading and verifying an image again cancels its earlier pending eviction. Failed verification preserves that cleanup intent.

Cleanup preserves volumes, backups, unrelated images and containers, and unknown files. Invalid artifact metadata prevents artifact deletion until the inventory can be read safely. This is targeted reclamation, not a host-wide Docker prune.

Controller logs report reclaimed file and byte counts, completed failed-workload cleanup groups, and whether cleanup remains pending for retry. Cleanup does not require a CLI command.
