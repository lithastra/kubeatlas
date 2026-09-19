#!/usr/bin/env python3
# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
"""Behavior tests using fake Helm; no registry, credentials, or cluster access."""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

WRAPPER = Path(__file__).with_name("anonymous_helm.py")
FAKE_HELM = r'''
import json, os, pathlib, signal, subprocess, sys, time
docker = pathlib.Path(os.environ["DOCKER_CONFIG"]) / "config.json"
registry = pathlib.Path(os.environ["HELM_REGISTRY_CONFIG"])
assert json.loads(docker.read_text()) == {"auths": {}}
assert json.loads(registry.read_text()) == {"auths": {}}
assert docker.stat().st_mode & 0o777 == 0o600
assert registry.stat().st_mode & 0o777 == 0o600
assert os.environ["KUBECONFIG"] == "preserve-kubeconfig"
report = {"docker": str(docker), "registry": str(registry), "args": sys.argv[1:]}
mode = os.environ.get("FAKE_MODE", "ok")
if mode in ("hang", "orphan"):
    child = subprocess.Popen([sys.executable, "-c",
        "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(120)"])
    report["child"] = child.pid
pathlib.Path(os.environ["FAKE_REPORT"]).write_text(json.dumps(report))
if mode == "hang":
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    time.sleep(120)
print("fake chart metadata", flush=True)
sys.exit(7 if mode == "fail" else 0)
'''


class AnonymousHelmTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        fake = self.root / "helm"
        fake.write_text(f"#!{sys.executable}\n" + FAKE_HELM)
        fake.chmod(0o700)
        self.poison = self.root / "original-config.json"
        self.original = json.dumps({"credsStore": "must-not-run", "auths": {"example": {"auth": "fake"}}})
        self.poison.write_text(self.original)
        self.env = dict(os.environ, PATH=f"{self.root}:{os.environ['PATH']}",
                        DOCKER_CONFIG=str(self.root), HELM_REGISTRY_CONFIG=str(self.poison),
                        KUBECONFIG="preserve-kubeconfig", FAKE_REPORT=str(self.root / "report.json"))
        (self.root / "config.json").write_text(self.original)

    def command(self, timeout="5"):
        return [sys.executable, str(WRAPPER), "--timeout", timeout, "--", "show", "chart", "public-chart"]

    def check_cleanup(self):
        report = json.loads((self.root / "report.json").read_text())
        self.assertFalse(Path(report["docker"]).exists())
        self.assertFalse(Path(report["registry"]).exists())
        self.assertEqual(self.poison.read_text(), self.original)
        self.assertEqual((self.root / "config.json").read_text(), self.original)
        self.assertEqual(report["args"], ["show", "chart", "public-chart"])
        if "child" in report:
            # A reparented zombie may remain until init reaps it on Linux.
            state = subprocess.run(["ps", "-o", "stat=", "-p", str(report["child"])],
                                   capture_output=True, text=True, check=False).stdout.strip()
            self.assertTrue(not state or state.startswith("Z"), state)

    def test_isolated_success(self):
        result = subprocess.run(self.command(), env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "fake chart metadata\n")
        self.check_cleanup()

    def test_preserves_failure(self):
        result = subprocess.run(self.command(), env=dict(self.env, FAKE_MODE="fail"), timeout=15)
        self.assertEqual(result.returncode, 7)
        self.check_cleanup()

    def test_timeout_kills_stubborn_child(self):
        result = subprocess.run(self.command("1"), env=dict(self.env, FAKE_MODE="hang"),
                                capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 124, result.stderr)
        self.assertIn("wall-clock timeout", result.stderr)
        self.check_cleanup()

    def test_success_cleans_orphan_child(self):
        result = subprocess.run(self.command(), env=dict(self.env, FAKE_MODE="orphan"), timeout=15)
        self.assertEqual(result.returncode, 0)
        self.check_cleanup()

    def test_cancellation_cleans_child(self):
        process = subprocess.Popen(self.command("30"), env=dict(self.env, FAKE_MODE="hang"))
        try:
            deadline = time.monotonic() + 5
            while not (self.root / "report.json").exists():
                self.assertLess(time.monotonic(), deadline)
                time.sleep(0.05)
            process.send_signal(signal.SIGTERM)
            self.assertEqual(process.wait(timeout=10), 143)
            self.check_cleanup()
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()

    def test_invalid_timeout(self):
        for value in ("0", "-1", "inf", "nan"):
            with self.subTest(value=value):
                result = subprocess.run(self.command(value), env=self.env, capture_output=True, timeout=15)
                self.assertEqual(result.returncode, 2)
                self.assertFalse((self.root / "report.json").exists())


if __name__ == "__main__":
    unittest.main()
