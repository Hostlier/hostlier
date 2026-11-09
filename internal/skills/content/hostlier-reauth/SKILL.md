---
name: hostlier-reauth
description: Compatibility alias for renewing a saved Hostlier connection with hostlier connect PROFILE.
---

# Hostlier reauth

`hostlier reauth PROFILE` is an alias for `hostlier connect PROFILE`. Omit PROFILE to renew the selected server. Both use the same target resolution, remembered authentication method, fresh approval and atomic profile update. Prefer `connect` when describing the common setup and renewal workflow.

Renewal uses the saved HTTPS origin without relying on the expired token. Cancellation or failed approval leaves the profile, credential and saved authentication method intact. Successful renewal preserves the selected server. It does not revoke earlier server credentials; those retain their original expiry/revocation policy.

Advanced authentication uses the same `--auth` selector and compatibility flags documented by `hostlier skills show connect`. An owner must perform their own passkey or email-code prompt. Do not ask for credential material in chat. If a profile was removed, use `hostlier connect HTTPS_URL --name PROFILE` to create it again. See `docs/guides/connect.md` for the complete shared flow.
