"""The generated-type check rejects an API wrapper that sends a command the desktop will not forward."""

import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import check_generated_types as check


class GeneratedTypesTests(unittest.TestCase):
    def test_ui_whitelist_is_read_from_the_rust_contract(self):
        commands = check.ui_commands()
        self.assertIn("search.query", commands)
        self.assertNotIn("daemon.shutdown", commands)
        self.assertNotIn("media.lease", commands)

    def test_wrapper_commands_outside_the_whitelist_are_reported(self):
        original = check.API
        with tempfile.TemporaryDirectory() as folder:
            fake = Path(folder) / "api.ts"
            fake.write_text('call("search.query", {}); call("daemon.shutdown", {});', encoding="utf-8")
            check.API = fake
            try:
                self.assertEqual(
                    check.unknown_commands(),
                    ["api.ts sends a command outside UI_COMMANDS: daemon.shutdown"],
                )
            finally:
                check.API = original

    def test_current_wrapper_only_sends_whitelisted_commands(self):
        self.assertEqual(check.unknown_commands(), [])


if __name__ == "__main__":
    unittest.main()
