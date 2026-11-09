# Recent logs and live following

Select a saved controller with `hostlier server use NAME`, then read application logs:

```sh
hostlier logs my-app --tail 50
hostlier logs my-app --tail 50 --follow
hostlier logs my-app --tail 0 --follow
hostlier logs my-app --component worker --tail 100
hostlier controller logs --operation OPERATION_ID --tail 20
```

`--tail N` selects the latest N lines. For structured controller logs, it selects
the latest N matching events after the operation filter. Counts must be integers
from 0 through 10000. Application and sidecar logs default to 200 lines when the
flag is omitted; controller event defaults remain unchanged.

`--follow` polls for new output until Ctrl-C. With `--tail 0 --follow`, existing
application output is used as a baseline but is not printed. When the bounded
text window advances, overlapping lines are suppressed. This is snapshot polling,
not a durable log subscription: very high output rates can exceed the retained
window between polls.

Docker and Kubernetes receive the selected line count. Application and resource
responses remain capped at 64 KiB after secret redaction, so very long lines may
produce fewer lines than requested. Resource logs also accept `--tail`, within
the history made available by their provider backend. Component selection and
existing access controls still apply.

The CLI and controller must both include tail support to retrieve more than an
older controller's default history. The CLI still trims shorter requested tails
when talking to an older controller.
