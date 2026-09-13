"""Behavioral regression tests: virtual CNPG lifecycle, real chaos script/jq."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
MOCK = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
p = Path(os.environ["MOCK_STATE"])
s = json.loads(p.read_text())
args = sys.argv[1:]
cmd = Path(sys.argv[0]).name
mode = os.environ["MOCK_MODE"]
def save(): p.write_text(json.dumps(s))
def pod(uid, ready=True):
    return {"metadata":{"name":"kubeatlas-pg-1","uid":uid},"status":{"conditions":[{"type":"Ready","status":"True" if ready else "False"}]}}
stopped = s.get("on") and s["time"] - s.get("start", s["time"]) >= 180
if cmd == "date":
    print(s["time"])
elif cmd == "sleep":
    s["time"] += int(args[0]); save()
elif cmd == "curl":
    assert "--max-time" in args and "--connect-timeout" in args
    url = args[-1]
    if mode == "transport-failure" and stopped: sys.exit(28)
    if url.endswith("/metrics"):
        reachable = not stopped or mode == "no-outage"
        if mode == "no-recovery" and s.get("resumed"): reachable = False
        print("kubeatlas_storage_reachable " + str(int(reachable)))
        if mode != "missing-panic": print("kubeatlas_rego_eval_panic_total 0")
    else: print("{}")
elif cmd == "kubectl":
    assert args.pop(0) == "--request-timeout=10s"
    if args[0] == "annotate":
        s.setdefault("writes", []).append(args[-1])
        if args[-1] == "cnpg.io/hibernation=on":
            s["on"] = True; s["start"] = s["time"]
        else:
            s["on"] = False; s["resumed"] = True
        save()
        if mode == "ambiguous-write" and args[-1].endswith("=on"): sys.exit(1)
    elif args[1] == "cluster.postgresql.cnpg.io":
        annotation = "on" if s.get("on") else ("off" if mode == "original-off" else "")
        print(json.dumps({"metadata":{"annotations":{"cnpg.io/hibernation":annotation}},"spec":{"instances":2 if mode == "ha" else 1,"stopDelay":1800},"status":{"readyInstances":1,"conditions":[{"type":"cnpg.io/hibernation","status":"True" if stopped else "False"}]}}))
    elif args[1] == "pods":
        selector = args[args.index("-l")+1]
        if selector.startswith("app."):
            app_uid = "new-app" if mode == "app-restart" and s.get("resumed") else "app"
            print(json.dumps({"items":[{"metadata":{"uid":app_uid,"labels":{"pod-template-hash":"abc"}},"status":{"containerStatuses":[{"name":"kubeatlas","restartCount":0}]}}]}))
        elif stopped: print('{"items":[]}')
        else:
            uid = "old" if not s.get("resumed") or mode == "same-uid" else "new"
            print(json.dumps({"items":[pod(uid, mode != "not-ready" or not s.get("resumed"))]}))
    else: sys.exit("unexpected kubectl command: " + str(args))
else: sys.exit("unexpected mock: " + cmd)
'''


class PGDisconnectTest(unittest.TestCase):
    def run_scenario(self, mode, success):
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory)
            mock = temp / "mock"
            mock.write_text(MOCK)
            mock.chmod(0o755)
            for cmd in ("kubectl", "curl", "date", "sleep"):
                (temp / cmd).symlink_to(mock)
            state = temp / "state.json"
            state.write_text(json.dumps({"time":1000}))
            result = temp / "result.json"
            env = dict(os.environ, PATH=f"{temp}:{os.environ['PATH']}", MOCK_MODE=mode,
                       MOCK_STATE=str(state), KUBEATLAS_CHAOS_RESULT_FILE=str(result),
                       KUBEATLAS_TIER="tier2")
            run = subprocess.run(["bash", "test/chaos/pg-disconnect.sh"], cwd=ROOT,
                                 env=env, capture_output=True, text=True, timeout=30)
            self.assertEqual(run.returncode == 0, success, run.stdout + run.stderr)
            final = json.loads(state.read_text())
            self.assertFalse(final.get("on", False), "failure left PostgreSQL hibernating")
            self.assertEqual(result.exists(), success, "failed scenario produced pass evidence")
            if success:
                evidence = json.loads(result.read_text())
                self.assertEqual(evidence["original_primary"], evidence["replacement_primary"])
                self.assertNotEqual(evidence["original_primary_uid"], evidence["replacement_primary_uid"])
                self.assertTrue(evidence["outage_observed"])
                self.assertGreaterEqual(evidence["shutdown_seconds"], 180)
            if mode == "original-off":
                self.assertEqual(final["writes"][-1], "cnpg.io/hibernation=off")
            if mode in ("ha", "missing-panic"):
                self.assertFalse(final.get("writes"), "preflight failure mutated cluster")

    def test_smart_shutdown_and_reused_name(self):
        self.run_scenario("normal", True)

    def test_preserves_explicit_off_annotation(self):
        self.run_scenario("original-off", True)

    def test_unobserved_outage_fails_and_resumes(self):
        self.run_scenario("no-outage", False)

    def test_replacement_must_be_ready(self):
        self.run_scenario("not-ready", False)

    def test_replacement_must_have_new_uid(self):
        self.run_scenario("same-uid", False)

    def test_recovery_must_be_observed(self):
        self.run_scenario("no-recovery", False)

    def test_transport_failure_resumes(self):
        self.run_scenario("transport-failure", False)

    def test_ambiguous_annotation_write_resumes(self):
        self.run_scenario("ambiguous-write", False)

    def test_application_replacement_is_rejected(self):
        self.run_scenario("app-restart", False)

    def test_ha_is_rejected_before_injection(self):
        self.run_scenario("ha", False)

    def test_missing_metric_is_not_zero(self):
        self.run_scenario("missing-panic", False)


if __name__ == "__main__":
    unittest.main()
