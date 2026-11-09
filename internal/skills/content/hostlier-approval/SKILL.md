---
name: hostlier-approval
description: Resume observation and execution of an exact Hostlier request after independent human approval.
---

# Hostlier approval

Use `hostlier approval REQUEST_ID --watch` with the same device/profile that proposed it. This never grants approval. It waits for the controller to verify a human signature or applicable deployment window, then executes the immutable retained payload. Completed requests return their retained result. Inspect an uncertain executing request rather than creating a duplicate mutation.
