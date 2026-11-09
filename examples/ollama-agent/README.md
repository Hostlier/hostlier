# Ollama Cloud agent in Harnest

This example uses Harnest's managed ADK framework with `LiteLLMModel` to call
Ollama Cloud. Harnest discovers two tools: `calculate` and `utc_time`. Arithmetic
uses explicit operations; it does not evaluate model-generated code.

The model is `deepseek-v4.1-flash:1017`, selected from the authenticated Ollama
Cloud model list and verified with a live request on 2026-10-31. Set
`OLLAMA_MODEL` to another available model when updating the deployment.

## Runtime contract

- Linux AMD64 executable with an embedded Harnest/Python runtime.
- Port 1907; public readiness endpoint `/healthz`.
- Authenticated Harnest API, including `POST /responses`.
- `OLLAMA_API_KEY` is the provider credential. `AGENT_ACCESS_TOKEN` is a separate
  bearer credential for callers of this test agent.
- Hostlier resolves both credentials from stored secret references. They are
  absent from source, the executable, and the deployment YAML.
- Single replica, 0.5 CPU and 512 MiB memory. Ollama inference runs in the cloud.
- Startup allows 180 seconds. This small test configuration limits concurrent
  requests; overlapping requests may receive HTTP 503.
- Sessions and checkpoints use process memory and disappear on restart.
- No bundled browser playground. Use the API client below.

## Build and test

The build uses the native Harnest compiler and `harnest-1.7.0-py3-none-any.whl`.
The older Python-only Harnest CLI is not interchangeable with this compiler.
The local Docker image `hostlier-ollama-builder:20261031` contains the native
compiler at `/tmp/hostlier-harnest-cli`, its launcher at
`/tmp/hostlier-harnest-launcher`, the wheel under `/opt/`, and `uv` under
`/opt/build-tools/bin`. See the [customer-support builder](../../../hosting-sdk/examples/customer-support/Dockerfile.builder)
in the sibling Hosting SDK checkout for this builder contract.

From this example directory:

```sh
mkdir -p dist/v3
docker run --rm --platform linux/amd64 \
  --mount "type=bind,source=$PWD/agent,target=/src,readonly" \
  --mount "type=bind,source=$PWD/dist/v3,target=/out" \
  --mount "type=bind,source=$PWD/build-linux.sh,target=/build-linux.sh,readonly" \
  --mount "type=bind,source=$PWD/test-http.py,target=/test-http.py,readonly" \
  hostlier-ollama-builder:20261031 sh /build-linux.sh
```

The app declares MCP `>=2.2,<3`, required by the installed LiteLLM OpenAI path even
without MCP tools. The verified build resolved MCP 2.3.0, LiteLLM 1.106.6, and
Google ADK 2.13.0. The executable embeds its hash-locked runtime.

The script builds the runtime, compiles the executable, runs nine offline tool
tests, then starts the compiled server to check readiness and authentication
using a synthetic token. No provider credentials are needed for the build.

Verified executable SHA-256:
`f0d3dcafe33344190f2306001d6a9d25243486d738ccf89603d2be1eab3eff12`.

## Controller and CLI

Secrets are stored by the controller so the deployed agent can run independently
of a laptop. A suitably authorized laptop CLI can submit them over HTTPS:

```sh
hostlier secret set ollama-cloud-key --from-env OLLAMA_API_KEY
hostlier secret set ollama-agent-access --stdin
```

For initial server administration, the same commands can run over SSH with
`--socket /run/hostlier/controller.sock`. Secret values are passed through stdin
or environment variables, never as command-line arguments.

`hostlier connect https://CONTROLLER --name do-test` pairs the laptop and selects
the saved profile. `hostlier server use do-test` selects it again later. Ordinary
remote commands then need neither `--socket` nor `--server`.

A scoped deployment credential can use specifically authorized stored secrets,
but cannot change them. The controller fix included with this example permits
compiled-artifact uploads with client-isolated staging, a free-space reserve,
and independent archive/image verification. App and secret scope is still
checked at deployment planning and execution.

From the paired laptop:

```sh
catalog=https://github.com/Usefused/hostlier-packages.git
hostlier package available --repository "$catalog"
hostlier package install harnest --repository "$catalog" --version COMMIT_FROM_LIST
mkdir -p agent/.harnest/build
cp dist/v3/ollama-agent agent/.harnest/build/
cp agent.hostlier.yaml agent/.hostlier
cd agent
hostlier package add harnest
hostlier plan
hostlier deploy
hostlier status ollama-agent
hostlier logs ollama-agent
```

## Call the agent

Use `AGENT_ACCESS_TOKEN` in your environment or a protected token file:

```sh
python3 ask.py --token-file /path/to/protected-token \
  'Use calculate to multiply 17 by 23, then tell me the result.'
```

The client sends the bearer credential only to the selected HTTPS endpoint.
The deployment's default address is
`https://agent.example.com`. The provider API key is not a caller token.

The deployed agent completed a real Ollama `calculate(17, "multiply", 23)` call
and answered `17 × 23 = 391`. Sequential HTTPS checks returned health 200 and
401 for both missing and incorrect bearer tokens. See the
[command walkthrough](deployment-walkthrough.md) for commands and observed outputs.
