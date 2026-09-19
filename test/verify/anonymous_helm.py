#!/usr/bin/env python3
# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
"""Run Helm with empty registry credentials and a wall-clock deadline.

Helm may fall back to Docker credential helpers even for public OCI charts.
Never read or modify the caller's credential files. A separate process group
lets timeouts and cancellation also stop any helper spawned by Helm.
"""

import argparse
import json
import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time


def positive_seconds(value):
    seconds = float(value)
    if not math.isfinite(seconds) or seconds <= 0:
        raise argparse.ArgumentTypeError("timeout must be positive and finite")
    return seconds


def stop_group(process):
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    # A helper can ignore TERM even after the Helm parent has exited.
    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        process.poll()
        try:
            os.killpg(process.pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.05)
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timeout", type=positive_seconds, required=True)
    parser.add_argument("helm_args", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    helm_args = args.helm_args
    if helm_args[:1] == ["--"]:
        helm_args = helm_args[1:]
    if not helm_args:
        parser.error("a Helm command is required")
    cancelled = []

    def cancel(signum, _frame):
        cancelled.append(signum)

    previous = {sig: signal.signal(sig, cancel) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        with tempfile.TemporaryDirectory(prefix="kubeatlas-anonymous-helm-") as temp:
            docker_dir = Path(temp) / "docker"
            docker_dir.mkdir(mode=0o700)
            registry_file = Path(temp) / "registry.json"
            for path in (docker_dir / "config.json", registry_file):
                path.write_text(json.dumps({"auths": {}}), encoding="utf-8")
                path.chmod(0o600)
            env = dict(os.environ, DOCKER_CONFIG=str(docker_dir),
                       HELM_REGISTRY_CONFIG=str(registry_file))
            process = subprocess.Popen(["helm", *helm_args], env=env, start_new_session=True)
            deadline = time.monotonic() + args.timeout
            try:
                while process.poll() is None:
                    if cancelled:
                        return 128 + cancelled[0]
                    if time.monotonic() >= deadline:
                        print("anonymous Helm command exceeded its wall-clock timeout", file=sys.stderr)
                        return 124
                    time.sleep(0.05)
                return process.returncode if process.returncode >= 0 else 128 - process.returncode
            finally:
                stop_group(process)
    except OSError as exc:
        print(f"unable to run anonymous Helm command: {exc.strerror}", file=sys.stderr)
        return 1
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)


if __name__ == "__main__":
    sys.exit(main())
