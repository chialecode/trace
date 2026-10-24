import sys
import types
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import quick_verify


def result(code, stdout="", stderr=""):
    return types.SimpleNamespace(returncode=code, stdout=stdout, stderr=stderr)


class QuickVerifyTests(unittest.TestCase):
    def test_quick_checks_pass_and_preserve_the_actual_command_tail(self):
        calls = []

        def runner(command, **kwargs):
            calls.append((command, kwargs))
            return result(0, stdout="quick evidence")

        code, results = quick_verify.execute([["first"], ["second"]], runner)
        self.assertEqual(code, 0)
        self.assertEqual([item["command"] for item in results], [["first"], ["second"]])
        self.assertEqual(results[0]["outputTail"], "quick evidence")
        self.assertEqual(calls[0][1]["cwd"], quick_verify.ROOT)

    def test_a_quick_failure_is_nonzero_and_stops_before_later_checks(self):
        calls = []

        def runner(command, **kwargs):
            calls.append(command)
            if command == ["second"]:
                return result(7, stderr="deliberate failure")
            return result(0)

        code, results = quick_verify.execute([["first"], ["second"], ["third"]], runner)
        self.assertEqual(code, 7)
        self.assertEqual(calls, [["first"], ["second"]])
        self.assertEqual(results[-1]["exitCode"], 7)
        self.assertIn("deliberate failure", results[-1]["outputTail"])

    def test_quick_suites_exclude_long_benchmarks_real_sites_and_package_builds(self):
        commands = quick_verify.suite_commands("all", ["subscription_evidence"])
        text = "\n".join(" ".join(command) for command in commands)
        self.assertNotIn("stage.py", text)
        self.assertNotIn("real_site_check.py", text)
        self.assertNotIn("bench", text)
        self.assertNotIn("package_windows.py", text)
        self.assertIn("subscription_evidence", text)
        self.assertIn("test", text)


if __name__ == "__main__":
    unittest.main()
