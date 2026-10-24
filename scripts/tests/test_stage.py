"""The stage entry fails closed on a malformed report and needs a run before packaging."""

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
STAGE = ROOT / "scripts" / "stage.py"


def stage(*args):
    return subprocess.run([sys.executable, str(STAGE), *args], capture_output=True, text=True, encoding="utf-8", cwd=ROOT)


class StageEntryTests(unittest.TestCase):
    def test_check_rejects_a_report_without_evidence(self):
        with tempfile.TemporaryDirectory() as folder:
            report = Path(folder) / "report.json"
            report.write_text(json.dumps({"status": "passed"}), encoding="utf-8")
            result = stage("check", str(report))
        self.assertEqual(result.returncode, 1)
        self.assertIn("[FAIL]", result.stdout)

    def test_package_without_a_run_names_the_missing_step(self):
        result = stage("package", "--run", "does-not-exist")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("is not an evidence run", result.stderr + result.stdout)


if __name__ == "__main__":
    unittest.main()
