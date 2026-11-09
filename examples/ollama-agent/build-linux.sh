#!/bin/sh
# Runs inside the laptop's Linux AMD64 builder; never on the controller.
set -eu
export PATH="/opt/build-tools/bin:$PATH"
export PYTHONDONTWRITEBYTECODE=1
export LITELLM_LOCAL_MODEL_COST_MAP=True
cp -R /src /tmp/ollama-source
agent=/tmp/ollama-source
cli=/tmp/hostlier-harnest-cli
"$cli" runtime build --agent "$agent" --output /tmp/ollama-runtime \
  --wheel /opt/harnest-1.7.0-py3-none-any.whl
"$cli" compile "$agent" --format executable --runtime /tmp/ollama-runtime \
  --embed-runtime --output /out/ollama-agent
digest=$(python3 -c 'import json; print(json.load(open("/tmp/ollama-runtime/runtime-manifest.json"))["digest"])')
runtime_root="/root/.cache/harnest/agents/runtimes/$digest"
uv pip install --python "$runtime_root/python/bin/python3.12" \
  --target /tmp/test-deps pytest pytest-asyncio
PYTHONPATH="$runtime_root/packages:/tmp/test-deps" \
  "$runtime_root/python/bin/python3.12" -m harnest.cli test "$agent"
# Preserve the compiler-generated dependency lock with the source.
if [ -f "$agent/harnest-runtime.lock" ]; then
  cp "$agent/harnest-runtime.lock" /out/harnest-runtime.lock
fi
sha256sum /out/ollama-agent
python3 /test-http.py
