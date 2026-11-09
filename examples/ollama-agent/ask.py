"""Call the deployed Harnest agent; credentials never enter command arguments."""

import argparse
import json
import os
from pathlib import Path
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("prompt")
parser.add_argument("--url", default="https://agent.example.com")
parser.add_argument("--token-file", type=Path)
args = parser.parse_args()
token = args.token_file.read_text().strip() if args.token_file else os.environ.get("AGENT_ACCESS_TOKEN", "")
if not token:
    parser.error("Set AGENT_ACCESS_TOKEN or use --token-file with a protected file")
if not args.url.startswith("https://"):
    parser.error("Use an HTTPS endpoint")
request = urllib.request.Request(
    args.url.rstrip("/") + "/responses",
    data=json.dumps({"input": args.prompt, "stream": False}).encode(),
    headers={"Content-Type": "application/json", "Authorization": "Bearer " + token},
)
try:
    with urllib.request.urlopen(request, timeout=130) as response:
        print(json.dumps(json.load(response), indent=2))
except urllib.error.HTTPError as error:
    print("Agent request failed with HTTP", error.code)
    raise SystemExit(1)
