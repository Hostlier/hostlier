"""Exercise the compiled server's authentication with synthetic credentials."""

import json
import os
import subprocess
import time
import urllib.error
import urllib.request

token = "synthetic-local-http-test-token"
env = dict(os.environ, AGENT_ACCESS_TOKEN=token, PYTHONDONTWRITEBYTECODE="1",
           LITELLM_LOCAL_MODEL_COST_MAP="True")
process = subprocess.Popen(["/out/ollama-agent", "serve", "--host", "127.0.0.1", "--port", "1907"], env=env)


def request(path, credential=None):
    headers = {} if credential is None else {"Authorization": "Bearer " + credential}
    req = urllib.request.Request("http://127.0.0.1:1907" + path, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=3) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


try:
    for _ in range(120):
        if process.poll() is not None:
            raise RuntimeError("Compiled agent exited before readiness")
        try:
            if request("/healthz")[0] == 200:
                break
        except (OSError, TimeoutError):
            pass
        time.sleep(1)
    else:
        raise RuntimeError("Compiled agent did not become ready")
    assert request("/sessions")[0] == 401
    assert request("/sessions", "wrong-token")[0] == 401
    status, body = request("/sessions", token)
    assert status == 200, (status, body)
    assert token.encode() not in body
    print(json.dumps({"health": 200, "missing_token": 401, "wrong_token": 401,
                      "valid_token": 200, "credential_not_echoed": True}))
finally:
    process.terminate()
    try:
        process.wait(timeout=15)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()
