---
name: hostlier-pairing
description: Manage Hostlier enrollment tokens and approval requests as a server administrator.
---

# Hostlier pairing

On the server, use the protected administration socket explicitly when appropriate:

```sh
sudo hostlier pairing create --name laptop --socket /run/hostlier/controller.sock
sudo hostlier pairing list --socket /run/hostlier/controller.sock
sudo hostlier pairing approve --socket /run/hostlier/controller.sock
```

Token creation issues short-lived secret enrollment material for the intended recipient. Keep it out of public logs and chat; deliver it through the user's authorized channel. For request-based enrollment, compare the requester's displayed code during the interactive approval workflow. `hostlier pairing cancel ID` invalidates a selected pending enrollment.

Enrollment grants administrator access. Act only for the requested identity and do not approve unfamiliar pending requests. Revoking an already enrolled identity uses `hostlier clients revoke ID`, not pairing cancellation.

For a bounded deployment connection, use `hostlier pairing code REQUEST_ID --app APP --for 2h --max-uses 20 --secrets NAME --create-secrets NAME --socket PATH`. Creation permission is optional and permits only missing names also approved for use. Modern waiting CLIs finish automatically after this explicit administrator approval. Legacy enrollment tokens and scoped device grants confer different authority; use the latter for deployment-only access.

For all-access pairing, `hostlier pairing code REQUEST_ID --socket PATH` omits every permission flag. This grants full remote administration with no expiry or usage ceiling, still bound to the device and revocable. Browser pairing has the same semantics when every permission field is blank; the owner must approve the displayed all-access selection. Never silently convert partial or invalid restrictions to all access.
