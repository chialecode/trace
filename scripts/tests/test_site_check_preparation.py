"""Local-only credential preparation; fixtures never contact a real site."""
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from prepare_site_check import PreparationError, prepare


class SiteCheckPreparationTests(unittest.TestCase):
    def setup_files(self, root):
        (root / '.local/site-secrets').mkdir(parents=True)
        (root / 'package/adapters').mkdir(parents=True)
        (root / 'package/trace-daemon.exe').touch()
        (root / 'package/adapters/trace-adapter-bilibili.exe').touch()
        (root / '.env.local').write_text('UNRELATED_KEY=do-not-use\nTRACE_TEST_BILIBILI_COOKIE_FILE=.local/site-secrets/bilibili.cookie\nTRACE_TEST_DOUYIN_COOKIE_FILE=missing\n', encoding='utf8')

    def test_missing_credentials_stop_before_any_output(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            with self.assertRaises(PreparationError):
                prepare(root, 'bilibili', '.local/site-check/new', 'package')
            self.setup_files(root)
            with self.assertRaises(PreparationError):
                prepare(root, 'bilibili', '.local/site-check/new', 'package')
            self.assertFalse((root / '.local/site-check').exists())

    def test_preparation_keeps_secret_out_of_output_and_requires_new_scope(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            marker = 'synthetic_cookie=local-fixture-only'
            (root / '.local/site-secrets/bilibili.cookie').write_text(marker, encoding='utf8')
            result = prepare(root, 'bilibili', '.local/site-check/new', 'package')
            self.assertEqual(result['networkRequests'], 0)
            self.assertFalse(result['executionAuthorized'])
            self.assertFalse(result['loginVerified'])
            self.assertNotIn(marker, json.dumps(result))
            request = json.loads((root / '.local/site-check/new/request.json').read_text(encoding='utf8'))
            self.assertEqual(request['requests'], [{'method': 'probe_auth', 'params': {}}])
            self.assertEqual(request['max_http_requests'], 3)
            for p in (root / '.local/site-check/new').glob('*.json'):
                self.assertNotIn(marker, p.read_text(encoding='utf8'))
            with self.assertRaises(PreparationError):
                prepare(root, 'bilibili', '.local/site-check/new', 'package')
            with self.assertRaises(PreparationError):
                prepare(root, 'bilibili', 'outside', 'package')

    def test_duplicate_and_malformed_inputs_are_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            cookie = root / '.local/site-secrets/bilibili.cookie'
            for value in ['', 'Cookie: synthetic=value', 'a=b\nc=d', 'x' * 16385]:
                cookie.write_text(value, encoding='utf8')
                with self.assertRaises(PreparationError):
                    prepare(root, 'bilibili', '.local/site-check/new', 'package')
            cookie.write_text('synthetic=value', encoding='utf8')
            with (root / '.env.local').open('a', encoding='utf8') as f:
                f.write('TRACE_TEST_BILIBILI_COOKIE_FILE=duplicate\n')
            with self.assertRaises(PreparationError):
                prepare(root, 'bilibili', '.local/site-check/new', 'package')
            self.assertFalse((root / '.local/site-check').exists())
