"""Regression checks for documentation and publication boundaries, using isolated copies."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from check_docs import validate as check_docs
from check_publication import validate as check_publication

ROOT = Path(__file__).resolve().parents[2]


class GovernanceTests(unittest.TestCase):
    def doc_copy(self, folder):
        target = Path(folder)
        shutil.copytree(ROOT / 'docs', target / 'docs')
        for path in ROOT.glob('*.md'):
            shutil.copyfile(path, target / path.name)
        return target

    def test_broken_link_and_missing_registry_are_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            target = self.doc_copy(folder)
            (target / 'docs' / 'unregistered.md').write_text('[broken](missing.md#gone)', encoding='utf8')
            errors, _ = check_docs(target)
            self.assertTrue(any('not registered' in x for x in errors))
            self.assertTrue(any('broken link' in x for x in errors))

    def test_competing_plan_and_invalid_anchor_are_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            target = self.doc_copy(folder)
            registry_path = target / 'docs/governance/document-registry.json'
            data = json.loads(registry_path.read_text(encoding='utf8'))
            for entry in data['documents']:
                if entry['path'] == 'docs/delivery/m0-m1a-execution-plan.md':
                    entry.update(kind='execution-plan', status='active')
            registry_path.write_text(json.dumps(data), encoding='utf8')
            with (target / 'README.md').open('a', encoding='utf8') as out:
                out.write('\n[invalid](docs/README.md#nonexistent-anchor)\n')
            errors, _ = check_docs(target)
            self.assertTrue(any('exactly one active' in x for x in errors))
            self.assertTrue(any('missing anchor' in x for x in errors))

    def test_removed_requirement_is_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            target = self.doc_copy(folder)
            path = target / 'docs/requirements.md'
            text = path.read_text(encoding='utf8')
            path.write_text('\n'.join(line for line in text.splitlines() if not line.startswith('| FR-ID-001 |')), encoding='utf8')
            errors, _ = check_docs(target)
            self.assertTrue(any('FR-ID-001' in x for x in errors))

    def test_staged_private_path_stays_detected_after_worktree_fix(self):
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder)
            subprocess.run(['git', 'init', '-q', folder], check=True)
            path = target / 'note.md'
            private_path = 'D:' + '/Code/' + 'private-reference'
            path.write_text(private_path, encoding='utf8')
            subprocess.run(['git', '-C', folder, 'add', 'note.md'], check=True)
            path.write_text('sanitized', encoding='utf8')
            self.assertTrue(any(x.startswith('index:note.md') for x in check_publication(target)))
            self.assertFalse(any(x.startswith('worktree:note.md') for x in check_publication(target)))

    def test_ignored_files_not_read_but_forced_tracking_is_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder)
            subprocess.run(['git', 'init', '-q', folder], check=True)
            (target / '.gitignore').write_text('.env.local\n', encoding='utf8')
            (target / '.env.local').write_text('EXAMPLE=local-only', encoding='utf8')
            self.assertEqual([], check_publication(target))
            subprocess.run(['git', '-C', folder, 'add', '-f', '.env.local'], check=True)
            self.assertTrue(any('private-file' in x for x in check_publication(target)))


if __name__ == '__main__':
    unittest.main()
