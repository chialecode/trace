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
    def test_requirement_status_and_scope_are_checked(self):
        with tempfile.TemporaryDirectory() as folder:
            target = self.doc_copy(folder)
            path = target / 'docs/delivery/requirements-map.json'
            original = json.loads(path.read_text(encoding='utf8'))

            def raw_errors(change):
                data = json.loads(json.dumps(original))
                change(data)
                path.write_text(json.dumps(data), encoding='utf8')
                return check_docs(target)[0]

            # The isolated copy holds only documents, so source files and root assets are missing there.
            baseline = set(raw_errors(lambda data: None))

            def errors_after(change):
                return [e for e in raw_errors(change) if e not in baseline]

            implemented = next(i for i, r in enumerate(original['requirements']) if r['status'] == 'implemented')
            pending = next(i for i, r in enumerate(original['requirements']) if r['status'] == 'pending')
            rows = lambda data: data['requirements']
            self.assertTrue(any('differs from requirements.md' in e for e in errors_after(lambda d: rows(d)[implemented].update(status='pending'))))
            self.assertTrue(any('not bound to the current plan' in e for e in errors_after(lambda d: rows(d)[implemented]['current'].update(plan='older plan'))))
            self.assertTrue(any('does not exist' in e for e in errors_after(lambda d: rows(d)[implemented]['current'].update(implementation=['no/such/file.rs']))))
            self.assertTrue(any('lacks implementation' in e for e in errors_after(lambda d: rows(d)[implemented]['current'].update(testCases=[]))))
            self.assertTrue(any('cannot claim' in e for e in errors_after(lambda d: rows(d)[pending]['current'].update(testCases=['x']))))
            self.assertTrue(any('applicability disagrees' in e for e in errors_after(lambda d: rows(d)[pending]['current'].update(applicable=True))))

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

    def test_real_test_targets_from_the_local_folder_cannot_be_published(self):
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder)
            subprocess.run(['git', 'init', '-q', folder], check=True)
            (target / '.gitignore').write_text('.local/\n', encoding='utf8')
            local = target / '.local/site-check'
            local.mkdir(parents=True)
            (local / 'stage-targets.json').write_text(json.dumps({'targets': {
                'x': {'profile': 'https://x.com/Private_Handle9', 'screen_name': 'Private_Handle9'},
                'weibo': {'profile': 'https://weibo.com/u/5550001234'}}}), encoding='utf8')
            (target / 'ok.rs').write_text('parse("https://x.com/profile/channel")\n', encoding='utf8')
            self.assertEqual([], check_publication(target))
            (target / 'leak.rs').write_text('assert_eq!(parse("https://x.com/private_handle9"), x);\n', encoding='utf8')
            (target / 'leak.md').write_text('uid 5550001234\n', encoding='utf8')
            found = check_publication(target)
            self.assertEqual(sorted(found), ['worktree:leak.md: private-target', 'worktree:leak.rs: private-target'])
            self.assertFalse(any('Private_Handle9' in x or '5550001234' in x for x in found), 'the finding never repeats the value')


if __name__ == '__main__':
    unittest.main()
