#!/usr/bin/env python3
"""Offline performance-runner authorization guards; never run a load profile."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
RUNNER = HERE / "performance-native.sh"
WORKFLOW = HERE.parents[2] / ".github/workflows/performance.yml"


class PerformanceRunnerGuards(unittest.TestCase):
    def diagnostic(self, path):
        source = RUNNER.read_text()
        program = source.split("<<'PYPERFPHASE'\n", 1)[1].split("\nPYPERFPHASE", 1)[0]
        return subprocess.run(["python3", "-I", "-", str(path)], input=program,
                              capture_output=True, text=True, timeout=5)

    def check_rejected(self, changes=None, opt_in=True):
        with tempfile.TemporaryDirectory(prefix="aster-performance-guard-") as temporary:
            scratch = Path(temporary)
            marker = scratch / "tool-was-executed"
            tools = scratch / "bin"
            tools.mkdir()
            go = tools / "go"
            go.write_text("#!/bin/sh\n: > \"$PERF_GUARD_MARKER\"\nexit 90\n")
            go.chmod(0o700)
            output = scratch / "must-not-exist"
            environment = dict(os.environ, GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted",
                GITHUB_REPOSITORY="xiaoqiqixi/betteridcsys", GITHUB_REF="refs/heads/main",
                GITHUB_EVENT_NAME="push", GITHUB_RUN_ID="123", GITHUB_RUN_ATTEMPT="1",
                GITHUB_SHA="a" * 40, ASTER_MYSQL_TEST_DSN="fixture-private-dsn-must-not-escape",
                PERF_GUARD_MARKER=str(marker), PATH=str(tools) + os.pathsep + os.environ["PATH"])
            environment.update(changes or {})
            arguments = ["bash", str(RUNNER), "--repo-root", str(scratch), "--output", str(output)]
            if opt_in:
                arguments.append("--allow-isolated-runner")
            result = subprocess.run(arguments, env=environment, capture_output=True, text=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(output.exists())
            self.assertFalse(marker.exists(), "guard executed a tool before rejecting the unauthorized context")
            self.assertNotIn("fixture-private-dsn-must-not-escape", result.stdout + result.stderr)
            self.assertNotIn(str(scratch), result.stdout + result.stderr)

    def test_explicit_opt_in_is_required_before_tools_or_output(self):
        self.check_rejected(opt_in=False)

    def test_local_and_self_hosted_are_rejected_before_tools_or_output(self):
        self.check_rejected({"GITHUB_ACTIONS": "false"})
        self.check_rejected({"RUNNER_ENVIRONMENT": "self-hosted"})

    def test_other_repository_branch_and_pull_request_are_rejected(self):
        self.check_rejected({"GITHUB_REPOSITORY": "example/other"})
        self.check_rejected({"GITHUB_REF": "refs/heads/other"})
        self.check_rejected({"GITHUB_EVENT_NAME": "pull_request"})

    def test_missing_fixed_run_and_source_identity_is_rejected(self):
        for key, value in (("GITHUB_RUN_ID", ""), ("GITHUB_RUN_ATTEMPT", "unknown"),
                           ("GITHUB_SHA", "main"), ("GITHUB_SHA", "a" * 40 + "\n")):
            with self.subTest(key=key, value=value):
                self.check_rejected({key: value})

    def test_workflow_retains_only_metrics_and_cleanup_receipts(self):
        source = WORKFLOW.read_text()
        start = source.index("      - name: Retain validated synthetic performance evidence")
        end = source.index("      - name: Remove owned native runner scratch", start)
        artifact = source[start:end]
        paths = [line.strip() for line in artifact.splitlines() if "${{ runner.temp }}/" in line]
        self.assertEqual(paths, [
            "${{ runner.temp }}/http-performance/performance.json",
            "${{ runner.temp }}/http-performance/runner-cleanup.json",
            "${{ runner.temp }}/http-performance/fixture-cleanup.json",
        ])
        self.assertNotIn(".log", artifact)
        self.assertIn("timeout-minutes: 25", source)
        self.assertIn("contents: read", source)
        self.assertNotIn("contents: write", source)
        self.assertNotIn("pull_request:", source)

    def test_failure_diagnostics_project_only_the_last_exact_known_stage(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "private.log"
            path.write_bytes(b"secret-dsn-and-response-must-not-escape\n"
                             b"ASTER_HTTP_PERF_STAGE=seed\n"
                             b"ASTER_HTTP_PERF_STAGE=secret-dsn-and-response-must-not-escape\n"
                             b"prefix ASTER_HTTP_PERF_STAGE=report\n"
                             b"ASTER_HTTP_PERF_STAGE=worker\n")
            path.chmod(0o600)
            result = self.diagnostic(path)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "PERFORMANCE_FAILURE_STAGE=worker\n")
            self.assertEqual(result.stderr, "")
            self.assertNotIn("secret-dsn", result.stdout)

    def test_failure_diagnostics_reject_links_shared_modes_and_oversized_logs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "private.log"
            path.write_text("ASTER_HTTP_PERF_STAGE=measurement\nsecret-must-not-escape\n")
            path.chmod(0o600)
            link = root / "link"
            link.symlink_to(path)
            result = self.diagnostic(link)
            self.assertEqual(result.stdout, "PERFORMANCE_FAILURE_STAGE=unknown\n")
            self.assertEqual(result.stderr, "")
            path.chmod(0o640)
            result = self.diagnostic(path)
            self.assertEqual(result.stdout, "PERFORMANCE_FAILURE_STAGE=unknown\n")
            path.chmod(0o600)
            path.write_bytes(b"ASTER_HTTP_PERF_STAGE=measurement\n" + b"x" * (1024 * 1024))
            result = self.diagnostic(path)
            self.assertEqual(result.stdout, "PERFORMANCE_FAILURE_STAGE=unknown\n")
            self.assertNotIn("secret-must-not-escape", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
