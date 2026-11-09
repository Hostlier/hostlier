# Hostlier command walkthrough

This is a historical deployment transcript. For the current catalog package flow, follow the [example instructions](README.md).


Commands below omit the temporary local CLI binary's absolute path for readability.
The laptop binary is `/private/tmp/hostlier-do-test-ed4236b/hostlier`.

## Store runtime secrets

These initial commands ran on the Droplet through SSH. Values were supplied
privately through stdin; only metadata was printed.

```sh
hostlier secret set ollama-cloud-key --stdin --socket /run/hostlier/controller.sock
hostlier secret set ollama-agent-access --stdin --socket /run/hostlier/controller.sock
```

Output:

```json
{"name":"ollama-cloud-key","revision":"4c9f8fb78674616a9e04784a833af86289ceee951de15901c99a400e2a593560"}
{"name":"ollama-agent-access","revision":"ebb670947e2dd4ddbe1ade43887b70bb655d77d48408fce26a44d9acf2e7883b"}
```

The controller retains these secrets for the running agent. An authorized laptop
CLI can submit secrets over HTTPS; storing them on the controller does not require
running the CLI there. A scoped deployment credential can use its allowed secret
references but cannot change their values.

## Validate and pair the laptop

The first uploaded manifest was validated on the Droplet:

```sh
hostlier validate --file /root/ollama-agent/agent.hostlier.yaml
```

```json
{"name":"ollama-agent","valid":true}
```

The laptop then requested a scoped connection:

```sh
hostlier connect https://controller.example.com --name do-test
```

It printed a review URL, request ID and device fingerprint, then prompted for a
one-time code. Using the existing SSH administrator connection:

```sh
hostlier pairing code 2dcf4854423fa6c5475991c7fc43ee9f6649646ef53648b8256ec6f084635fa4 \
  --app ollama-agent --secrets ollama-cloud-key,ollama-agent-access \
  --for 2h --max-uses 5 --socket /run/hostlier/controller.sock
```

The output confirmed that exact scope and returned a one-time code, which was
entered privately in the laptop prompt. The laptop returned:

```json
{"connected":true,"credentialStorage":"owner-only file fallback","name":"do-test","url":"https://controller.example.com"}
```

The connection became the selected profile automatically. Subsequent laptop
commands use it without `--socket` or `--server`. The grant lasts two hours;
expiry requires fresh authorization.

## Original upload failure and first deployment

```sh
hostlier plan --file examples/ollama-agent/agent.hostlier.yaml
```

```text
Packaging compiled Harnest artifact; application code is not executed
Hostlier: Request is outside this deployment credential's scope
```

The original controller omitted artifact endpoints from scoped credentials.
The initial import used server administration:

```sh
hostlier plan --file /root/ollama-agent/agent.hostlier.yaml \
  --socket /run/hostlier/controller.sock
```

The plan resolved image
`hostlier.local/artifacts@sha256:1961895fdff2ebe7dfcded02fa7e082895a0dea33d102bd4c1e9b649853cca78`,
0.5 CPU, 512 MiB memory, both secret references, and the agent HTTPS route, with
no warnings. The laptop then deployed an image-based copy of the manifest:

```sh
hostlier deploy --file examples/ollama-agent/dist/v1/deploy.hostlier.yaml
```

It returned operation
`c93396d164e89e4fada3da1bebb02f7727cdc4d7223726f8cece6e1aee6b9678`,
progress through route/certificate/readiness, and `status: succeeded`.

The first live model call failed despite readiness. This command exposed the
missing runtime dependency:

```sh
hostlier logs ollama-agent
```

Relevant final error:

```text
litellm.exceptions.APIConnectionError: APIConnectionError:
OpenAIException - No module named 'mcp'
```

The final rebuild includes the MCP version required by LiteLLM. A complete local
Harnest HTTP invocation then returned a real Ollama `calculate` tool call,
`{"result":391}`, and the answer `17 × 23 = 391`.

## Controller fix and direct deployment

The controller was rebuilt from `ed4236b` plus the scoped-artifact fix. The full
Go race suite, `go vet`, and the separate-executable dependency-boundary check
passed. Focused tests additionally cover two clients staging the same digest,
commit retries, malformed content, shared quota, expiry and revocation. The
repository quality checker reports pre-existing findings outside the changed files.

The previous controller executable is retained at
`/root/hostlier-controller-before-scoped-upload`. The fixed executable SHA-256 is
`4cc81b90561c44cf7e1b58470066698b94e3998bfdd8208ad6420223a452df76`.
After replacement and service restart, the controller health endpoint returned
HTTP 200 with `{"ok":true}`.

The final deployment uses the original native-artifact manifest directly from
the laptop:

```sh
hostlier deploy --file examples/ollama-agent/agent.hostlier.yaml
```

This packages and uploads the compiled executable through the selected scoped
HTTPS connection, then plans and deploys it. No manual server-side file transfer
or socket flag is part of this command.

The scoped upload and plan succeeded for image
`hostlier.local/artifacts@sha256:6579571ad894a11ef1e8f59c3b2a8953356bb1fa4a85c0b60377d88e5b63570d`.
Operation `96e5b8a5fa06b419db26bef2fac3358b520d389730c0f53dce7688a365c0101b`
then failed with `HEALTH_TIMEOUT` during replacement startup. The original
agent remained running.

## Remove the old version and complete deployment

At the user's request, server administration removed the old agent before retrying:

```sh
hostlier delete ollama-agent --socket /run/hostlier/controller.sock
```

```json
{"action":"delete","name":"ollama-agent","status":"succeeded"}
```

The obsolete Docker image, failed-rollout network container, stored archive and
original uploaded executable were also removed. The archive and executable
cleanup freed 309579874 bytes, in addition to the Docker image removal. Stored
runtime secrets were retained.

The native-artifact manifest now allows 180 seconds for startup on the small VPS.
Because the exact replacement image was already uploaded, the retry reused it:

```sh
hostlier deploy --file examples/ollama-agent/dist/v3/deploy.hostlier.yaml
```

```json
{"id":"ccaa21bd7b6c5ff9242906c22e473a702482a42b9f63479ae9b3e0ea731b318f","name":"ollama-agent","status":"succeeded"}
```

```sh
hostlier status ollama-agent
hostlier logs ollama-agent --tail 5
```

Status reported `running`, the expected image digest, and
`https://agent.example.com`. The tail command returned exactly five log
lines. Initial parallel checks produced concurrency-limit warnings; sequential
verification after the model call returned health 200, missing-token 401, and
wrong-token 401. The agent is intentionally configured for a small test workload.

The real HTTPS invocation completed with:

```json
{"tool":"calculate","arguments":{"left":17,"operator":"multiply","right":23},"result":391,"reply":"17 × 23 = 391"}
```

Log-tail support is installed in the laptop CLI and server. The final controller
SHA-256 is `5f1bb3ad296e349d7bfe70b7ae47468998a7318e88ad258ee4935bc3810b5fbb`.
The full race suite, static analysis, and executable-boundary checks passed after
the tail changes. See [log options](../../../hostlier/docs/guides/logs.md).

For further calls from the laptop, use the protected token file created for this
test; it is not committed to the repository:

```sh
python3 examples/ollama-agent/ask.py \
  --token-file /private/tmp/hostlier-ollama-build/agent-access-token \
  'Use calculate to multiply 17 by 23.'
```
