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
    def test_requirement_stage_order_and_names_are_checked(self):
        with tempfile.TemporaryDirectory() as folder:
            target = self.doc_copy(folder)
            path = target / 'docs/delivery/requirements-map.json'
            data = json.loads(path.read_text(encoding='utf8'))
            row = data['requirements'][0]
            row.update(firstStage='M10', completeStage='M9')
            path.write_text(json.dumps(data), encoding='utf8')
            errors, _ = check_docs(target)
            self.assertTrue(any('first stage after complete stage' in e for e in errors))
            row.update(firstStage='M9', completeStage='M10')
            path.write_text(json.dumps(data), encoding='utf8')
            errors, _ = check_docs(target)
            self.assertFalse(any('delivery stage' in e or 'first stage after' in e for e in errors))
            row.update(firstStage='M1a')
            path.write_text(json.dumps(data), encoding='utf8')
            errors, _ = check_docs(target)
            self.assertTrue(any('invalid delivery stage' in e for e in errors))

    def test_stage_retirement_is_numeric_and_rejects_expired_documents(self):
        with tempfile.TemporaryDirectory() as folder:
            target = self.doc_copy(folder)
            path = target / 'docs/governance/document-registry.json'
            data = json.loads(path.read_text(encoding='utf8'))
            data['currentStage'] = 'M10'
            entry = next(e for e in data['documents'] if e['path'] == 'README.md')
            entry['retireAfter'] = 'M9'
            path.write_text(json.dumps(data), encoding='utf8')
            errors, _ = check_docs(target)
            self.assertTrue(any('README.md: expired document' in e for e in errors))
            entry['retireAfter'] = 'M10'
            path.write_text(json.dumps(data), encoding='utf8')
            errors, _ = check_docs(target)
            self.assertFalse(any('README.md: expired document' in e for e in errors))
            data['currentStage'] = 'M1b'
            entry['retireAfter'] = 10
            path.write_text(json.dumps(data), encoding='utf8')
            errors, _ = check_docs(target)
            self.assertTrue(any('currentStage must' in e for e in errors))
            self.assertTrue(any('invalid retireAfter' in e for e in errors))

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
            # A second real fixture survives retirement of historical plans.
            plan = dict(next(e for e in data['documents'] if e['path'] == 'docs/delivery/stage-plan.md'))
            plan['path'] = 'docs/delivery/competing-fixture-plan.md'
            (target / plan['path']).write_text('# Competing plan\n', encoding='utf8')
            data['documents'].append(plan)
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
