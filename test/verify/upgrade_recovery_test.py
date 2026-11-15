#!/usr/bin/env python3
# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
"""Verify public upgrade baseline selection with fake Helm and Kubernetes."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

VERIFY_DIR = Path(__file__).resolve().parent
PUBLIC_CHART = "oci://ghcr.io/lithastra/charts/kubeatlas"
CHART_DIGEST = "sha256:1ba4fe34276337f70d5a5d160dea1a3f225c9d3950b6e869691219a7c528fce3"
APP_DIGEST = "sha256:ff185a897c10f244de6b014a868af4fecaa1f15c300c1de9a7b58a62a3b338c8"
DATABASE_IMAGE = (
    "ghcr.io/lithastra/postgres-age:16.15-age1.6.0-rc0.2"
    "@sha256:412ae56754e71aac96e8cb6327f70782c50bd4aeff75ff0ddf32eb4ee7400936"
)
INSTALL_FLAGS = [
    "--namespace", "recovery-test",
    "--set", "persistence.enabled=true",
    "--set", "persistence.embedded.enabled=true",
    "--set", "persistence.embedded.storageSize=1Gi",
    "--set", "snapshots.enabled=true",
]
FAKE_COMMAND = '''
import json, os, pathlib, sys

name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["MOCK_CALLS"], "a", encoding="utf-8") as calls:
    calls.write(json.dumps([name, *args]) + "\\n")

if name == "kubectl":
    if args[:2] == ["get", "deployment"]:
        sys.exit(42)  # Stop before any database, network, or restore operation.
    sys.exit(0)

if name == "helm":
    if args[:1] == ["pull"]:
        destination = pathlib.Path(args[args.index("--destination") + 1])
        (destination / "kubeatlas-1.6.0.tgz").touch()
    elif args[:2] == ["show", "chart"]:
        if "--version" in args:
            version = args[args.index("--version") + 1]
        else:
            assert pathlib.Path(args[2]).is_file()
            version = "1.6.0"
        print(f"version: {version}\\nappVersion: {version}")
    elif args[:1] == ["install"] and args[2].endswith(".tgz"):
        assert pathlib.Path(args[2]).is_file()
    sys.exit(0)

sys.exit(f"unexpected command: {name}")
'''


class UpgradeRecoveryTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        verify = self.root / "test" / "verify"
        verify.mkdir(parents=True)
        self.script = verify / "v160-upgrade-recovery.sh"
        original = (VERIFY_DIR / self.script.name).read_text(encoding="utf-8")
        # The production script uses fixed /tmp filenames for CI artifacts.
        # Relocate them in this copy so the EXIT trap only touches test files.
        self.script.write_text(original.replace("/tmp/kubeatlas-", "${TMPDIR}/kubeatlas-"),
                               encoding="utf-8")
        shutil.copyfile(VERIFY_DIR / "anonymous_helm.py", verify / "anonymous_helm.py")
        fake_bin = self.root / "bin"
        fake_bin.mkdir()
        for command in ("kubectl", "helm", "curl", "jq", "sha256sum"):
            fake = fake_bin / command
            fake.write_text(f"#!{sys.executable}\n" + FAKE_COMMAND, encoding="utf-8")
            fake.chmod(0o700)
        self.calls_file = self.root / "calls.jsonl"
        self.env = dict(os.environ, PATH=f"{fake_bin}{os.pathsep}{os.environ['PATH']}",
                        TMPDIR=str(self.root), MOCK_CALLS=str(self.calls_file),
                        KUBECONFIG=str(self.root / "unused-kubeconfig"),
                        KUBEATLAS_NAMESPACE="recovery-test", KUBEATLAS_RELEASE="recovery")
        self.env.pop("KUBEATLAS_UPGRADE_BASELINE", None)

    def run_verifier(self, baseline=None):
        env = self.env.copy()
        if baseline is not None:
            env["KUBEATLAS_UPGRADE_BASELINE"] = baseline
        return subprocess.run(["bash", str(self.script)], env=env, capture_output=True,
                              text=True, timeout=15, check=False)

    def check_install(self, baseline=None):
        result = self.run_verifier(baseline)
        self.assertEqual(result.returncode, 42, result.stdout + result.stderr)
        calls = [json.loads(line) for line in self.calls_file.read_text().splitlines()]
        install = next(index for index, call in enumerate(calls) if call[:2] == ["helm", "install"])
        self.assertEqual(calls[install + 1][:3], ["kubectl", "get", "deployment"])
        self.assertTrue(all(call[0] in ("kubectl", "helm") for call in calls), calls)
        return [call[1:] for call in calls[:install + 1] if call[0] == "helm"]

    def test_default_legacy_baseline_uses_versioned_oci_chart(self):
        chart_args = [PUBLIC_CHART, "--version", "1.5.2"]
        self.assertEqual(self.check_install(), [
            ["show", "chart", *chart_args],
            ["install", "recovery", *chart_args, *INSTALL_FLAGS, "--wait", "--timeout", "10m"],
        ])

    def test_v160_uses_digest_pinned_chart_and_images(self):
        pull, show, install = self.check_install("1.6.0")
        self.assertEqual(pull[:3], ["pull", f"{PUBLIC_CHART}@{CHART_DIGEST}", "--destination"])
        chart_dir = Path(pull[3])
        archive = str(chart_dir / "kubeatlas-1.6.0.tgz")
        self.assertEqual(show, ["show", "chart", archive])
        self.assertEqual(install, [
            "install", "recovery", archive, *INSTALL_FLAGS,
            "--set-string", f"image.digest={APP_DIGEST}",
            "--set-string", f"persistence.embedded.image={DATABASE_IMAGE}",
            "--wait", "--timeout", "10m",
        ])
        self.assertFalse(chart_dir.exists())

    def test_unsupported_baseline_never_runs_helm_or_kubectl(self):
        result = self.run_verifier("1.7.0")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("unsupported upgrade baseline: 1.7.0", result.stderr)
        self.assertFalse(self.calls_file.exists())


if __name__ == "__main__":
    unittest.main()
