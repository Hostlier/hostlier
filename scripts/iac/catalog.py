#!/usr/bin/env python3
"""Build a non-secret administrator catalog from already prepared immutable bytes."""
import argparse
import hashlib
import json
import pathlib
import platform
import stat


def manifest(root):
    """Reject redirected/untracked special files and enumerate every source/runtime byte."""
    files = {}
    for path in sorted(root.rglob("*")):
        mode = path.lstat().st_mode
        if path.is_symlink() or mode & 0o022:
            raise ValueError("bundle entries must not redirect or permit non-owner writes")
        if path.is_dir():
            continue
        if not stat.S_ISREG(mode):
            raise ValueError("bundle contains a non-regular file")
        if path.stat().st_size > 256 * 1024 * 1024:
            raise ValueError("bundle file exceeds protected executor limit")
        files[path.relative_to(root).as_posix()] = hashlib.sha256(path.read_bytes()).hexdigest()
    return files


def main():
    """Catalog construction is explicit local administration, never a workflow option."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=pathlib.Path, required=True)
    parser.add_argument("--name", required=True)
    parser.add_argument("--outputs", required=True, help="comma-separated finite output names")
    parser.add_argument("--secret", action="append", default=[], help="alias=credential environment variable")
    args = parser.parse_args()
    root = args.root.absolute()
    if root.resolve() != root or not root.is_dir() or root.stat().st_mode & 0o022:
        parser.error("use a canonical non-redirected administrator-owned bundle directory")
    operating_system = {"Darwin": "darwin", "Linux": "linux"}.get(platform.system())
    architecture = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine())
    if not operating_system or not architecture:
        parser.error("unsupported platform")
    mapping = dict(item.split("=", 1) for item in args.secret)
    catalog = {args.name: {"Bundle": {"Root": str(root), "Platform": f"{operating_system}/{architecture}", "Files": manifest(root)}, "Outputs": args.outputs.split(","), "SecretEnvironment": mapping}}
    print(json.dumps(catalog, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
