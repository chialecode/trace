"""The real-site driver's own logic, with synthetic inputs and no network or daemon."""
import copy
import hashlib
import json
import sys
import tempfile
import time
import types
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import real_site_check as rsc
import rule_evidence

SITES = ['weibo', 'xiaohongshu', 'instagram', 'x', 'youtube', 'tiktok', 'douyin', 'bilibili']
NAMES = ['微博', '小红书', 'Instagram', 'X', 'YouTube', 'TikTok', '抖音', '哔哩哔哩']
BUDGETS = {s: {'metadata_requests': 40, 'media_bytes': 512 * 1024 * 1024} for s in SITES}
USER_ACTIONS = ('<!-- ACT-007-AUTHORIZED-BUDGETS ' + json.dumps(BUDGETS) + ' -->\n\n**2026-10-16 A 的一次登录与目标探测**\n\n| 站点 | 已用请求 | 结果 |\n| --- | --- | --- |\n'
                + ''.join(f'| {n} | 2 | ok |\n' for n in NAMES) + '\n格式检查也通过。\n')
def synthetic_package(package: Path) -> None:
    package.mkdir()
    (package / 'profile.example.json').write_text(json.dumps({'test_allowed_hosts': [], 'extra_media_hosts': []}), encoding='utf8')
    (package / 'trace-daemon.exe').write_bytes(b'synthetic daemon')
    sources = {'crates/core/src/lib.rs': 'a' * 64}
    tree = hashlib.sha256(json.dumps(sources, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    manifest = {'git_commit': 'abc', 'source_tree_sha256': tree, 'source_files_sha256': sources,
                'sourceInputs': sources, 'schema_version': 8, 'control_protocol': '1',
                'build_commands': [{'argv': ['cargo', 'build'], 'exitCode': 0, 'sourceInputs': sources,
                                    'sourceUnchanged': True}]}
    (package / 'manifest.json').write_text(json.dumps(manifest), encoding='utf8')
    lines = []
    for path in sorted(package.rglob('*')):
        if path.is_file() and path.name != 'SHA256SUMS.txt':
            lines.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.relative_to(package).as_posix()}")
    (package / 'SHA256SUMS.txt').write_text('\n'.join(lines) + '\n', encoding='utf8')


SECRET = 'synthetic_cookie=local-fixture-only'


def chosen_by_range(span):
    """The full-run fake maps the exact preview date back to the listed work it chose."""
    start = str((span or {}).get('start', ''))
    mapping = {
        '2026-10-01': ('V1', '2026-10-01T08:00:00Z'),
        '2026-10-02': ('V2', '2026-10-02T08:00:00Z'),
        '2026-10-03': ('V3', '2026-10-03T08:00:00Z'),
        '2026-10-04': ('V4', '2026-10-04T08:00:00Z'),
    }
    for day, pair in mapping.items():
        if start.startswith(day):
            return pair
    return mapping['2026-10-01']


class IncrementalCoreFixture:
    """A stateful Core double whose evidence is derived from the same facts it reports."""

    def __init__(self, published='2026-10-01T08:00:00Z', media_after=0, relation='sub-1',
                 observations_after=1, second_endpoint='/list', new_second_job=False,
                 outside=False, save_progress='deferred', media_job=False, fail=None,
                 site='bilibili', native_key='BV1', account_key='42',
                 new_second_media_job=False):
        self.site, self.native_key, self.account_key = site, native_key, account_key
        self.published, self.media_after, self.relation = published, media_after, relation
        self.observations_after, self.second_endpoint = observations_after, second_endpoint
        self.new_second_job, self.new_second_media_job = new_second_job, new_second_media_job
        self.outside = outside
        self.save_progress, self.media_job, self.fail = save_progress, media_job, fail
        self.phase, self.calls, self.items = 0, [], []
        self.requests, self.jobs, self.contexts, self.acquisitions = [], [], [], []
        self.relations, self.observations = {}, {}
        self.received, self.evidence_media = 0, {}

    def _first_check(self):
        if self.fail:
            return
        self.items = [{
            'entry_id': 'e1', 'native_key': self.native_key, 'published_at': self.published,
            'save_progress': self.save_progress,
        }]
        self.relations['e1'] = ('t1', 't1')
        self.observations['e1'] = 1
        if self.outside:
            self.items.append({
                'entry_id': 'e2', 'native_key': 'BV2',
                'published_at': '2026-09-01T08:00:00Z', 'save_progress': 'deferred',
            })
            self.relations['e2'] = ('t1', 't1')
            self.observations['e2'] = 1
            self.contexts.append({
                'entry_id': 'e2', 'context_id': 'ctx-observe-e2',
                'job_id': None, 'job_generation': None, 'started_at': 't2',
                'auth_state': 'anonymous', 'basis': 'explicit_anonymous_scope',
            })
        self.requests = [
            {'endpoint': '/list', 'metadata': True, 'status': 200, 'started_at': 't1'},
            {'endpoint': '/detail', 'metadata': True, 'status': 200, 'started_at': 't2',
             'evidence_id': 'ctx-observe-e1'},
        ]
        self.jobs = [{
            'job_id': 'detail-1', 'job_key': 'observe:e1', 'job_type': 'observe',
            'subject_id': 'e1', 'state': 'succeeded', 'next_run_at': None, 'updated_at': 't3',
            'job_generation': 1, 'consumer_requested': 1, 'consumer_completed': 1,
            'consumer_state': 'succeeded',
        }]
        self.contexts = [
            context for context in self.contexts if context.get("entry_id") != "e1"
        ] + [{
            'entry_id': 'e1', 'context_id': 'ctx-observe-e1', 'job_id': 'detail-1',
            'job_generation': 1,
            'started_at': 't2', 'auth_state': 'anonymous', 'basis': 'explicit_anonymous_scope',
        }]
        if self.media_job:
            self.jobs.append({
                'job_id': 'media-1', 'job_key': 'media:e1', 'job_type': 'media',
                'subject_id': 'e1', 'state': 'succeeded', 'next_run_at': None,
                'updated_at': 't3', 'job_generation': 1, 'consumer_requested': 1,
                'consumer_completed': 1, 'consumer_state': 'succeeded',
            })
            self.contexts.append({
                'entry_id': 'e1', 'context_id': 'ctx-media-e1', 'job_id': 'media-1',
                'job_generation': 1,
                'started_at': 't3', 'auth_state': 'anonymous',
                'basis': 'explicit_anonymous_scope',
            })
            self.acquisitions.append({
                'acquisition_id': 'acq-e1', 'context_id': 'ctx-media-e1',
                'media_id': 'm1', 'variant_id': 'v1', 'outcome': 'acquired',
                'finished_at': 't3', 'measured_spec': {'height': 720},
            })
            self.requests.append({
                'endpoint': '<page-or-redirect>', 'metadata': False, 'status': 200,
                'started_at': 't3', 'evidence_id': 'ctx-media-e1',
            })
        self.received = self.media_after
        if self.media_job and self.media_after:
            self.evidence_media['ctx-media-e1'] = self.media_after

    def _evidence(self, subscription_id):
        entries = []
        for timeline_item in self.items:
            entry_id = timeline_item['entry_id']
            count = (
                self.observations_after
                if entry_id == 'e1' and self.phase
                else self.observations.get(entry_id, 1)
            )
            relation = self.relations[entry_id]
            entries.append({
                **timeline_item,
                'relation': {
                    'first_discovered_at': relation[0],
                    'last_discovered_at': relation[1],
                },
                'discovered_by_subscriptions': [
                    {
                        'subscription_id': self.relation,
                        'first_discovered_at': relation[0],
                        'last_discovered_at': relation[1],
                    }
                ],
                'observation_count': count,
                'latest_observation_seq': count,
                'observations': [
                    {
                        'seq': seq, 'access_state': 'available', 'observed_at': 't2',
                        'context_id': (
                            'ctx-observe-e1' if entry_id == 'e1'
                            else f'ctx-observe-{entry_id}'
                        ),
                    }
                    for seq in range(1, count + 1)
                ],
                'jobs': [
                    {**job, 'job_generation': job.get('job_generation', 1)}
                    for job in self.jobs if job.get('subject_id') == entry_id
                ],
                'contexts': [
                    context for context in self.contexts
                    if context.get('entry_id') == entry_id
                ],
                'acquisitions': (
                    self.acquisitions if entry_id == 'e1' else []
                ),
            })
        if self.relation != subscription_id:
            entries = []
        requests = [
            {key: value for key, value in request.items() if key != 'evidence_id'}
            | {'evidence_id': request['evidence_id'], 'method': request.get('method', 'GET')}
            for request in self.requests
            if request.get('evidence_id') and self.relation == subscription_id
        ]
        media = self.evidence_media if self.relation == subscription_id else {}
        return {
            'subscription_id': subscription_id,
            'entries': entries,
            'requests': requests,
            'evidence_media_bytes': media,
            'budget_available': True,
            'truncated': False,
        }

    def call(self, command, args, timeout=None):
        self.calls.append((command, args))
        if command == 'daemon.status':
            return {'acquisition_budget': {
                'metadata_request_count': len(self.requests),
                'media_received_bytes': self.received,
                'stopped': False,
                'requests': self.requests,
                'evidence_media_bytes': self.evidence_media,
            }}
        if command == 'archive.subscription.evidence':
            return self._evidence(args['subscription_id'])
        if command == 'job.list':
            if self.fail:
                return {'items': [{
                    'job_type': 'scan', 'state': 'failed', 'next_run_at': None,
                    'last_error': {'class': self.fail},
                }]}
            return {'items': self.jobs}
        if command == 'site.catalog':
            lane = 'note' if self.site == 'xiaohongshu' else 'video'
            return {'sites': [{'id': self.site, 'rules': [{
                'declaration': {'id': f'{self.site}.user.{"notes" if lane == "note" else "videos"}', 'lane': lane},
            }]}]}
        if command == 'creator.create':
            return {'creator_id': 'cre-1'}
        if command == 'account.resolve':
            return {'native_account_key': self.account_key, 'display_name': 'someone'}
        if command == 'account.bind':
            return {'account_id': 'acct-1'}
        if command in ('subscription.create', 'subscription.update') and 'range' in args:
            self._first_check()
            return {'subscription_id': 'sub-1'} if command == 'subscription.create' else {}
        if command == 'subscription.check_now':
            self.phase = 1
            self.requests.append({
                'endpoint': self.second_endpoint, 'metadata': True, 'status': 200,
                'started_at': 't4',
            })
            if self.new_second_job:
                self.jobs.append({
                    'job_id': 'detail-2', 'job_key': 'observe:e1:2', 'job_type': 'observe',
                    'subject_id': 'e1', 'state': 'succeeded', 'next_run_at': None,
                    'updated_at': 't5', 'consumer_requested': 1, 'consumer_completed': 1,
                    'consumer_state': 'succeeded',
                })
            if self.new_second_media_job:
                self.jobs.append({
                    'job_id': 'media-2', 'job_key': 'media:e1:2', 'job_type': 'media',
                    'subject_id': 'e1', 'state': 'succeeded', 'next_run_at': None,
                    'updated_at': 't5', 'consumer_requested': 1, 'consumer_completed': 1,
                    'consumer_state': 'succeeded',
                })
            return {}
        if command == 'archive.timeline':
            return {'items': self.items}
        return {}


class IncrementalRunFixture:
    site, credential_id, account_id, account_key = 'bilibili', 'cred-1', None, '42'
    target = {'profile': 'https://example.com/42'}
    preview = {'items': [{
        'native_key': 'BV1', 'content_kinds': ['video'], 'video_count': 1,
        'image_count': 0, 'duration_ms': 30000, 'in_library': False, 'pinned': False,
        'published_at': '2026-10-01T08:00:00Z',
    }]}

    def __init__(self, core, used=()):
        self.core, self.used_keys = core, set(used)
        self.subscription_id = None

    def counters(self):
        return 5, self.core.received


class RealSiteCheckTests(unittest.TestCase):
    def setup_files(self, root: Path, sites=SITES):
        (root / '.local/site-secrets').mkdir(parents=True)
        (root / '.local/site-check').mkdir(parents=True)
        (root / 'USER-ACTIONS.md').write_text(USER_ACTIONS, encoding='utf8')
        env = ''.join(f'TRACE_TEST_{s.upper()}_COOKIE_FILE=.local/site-secrets/{s}.cookie\n' for s in sites)
        (root / '.env.local').write_text('UNRELATED_KEY=do-not-use\n' + env + 'TRACE_TEST_PROXY=http://127.0.0.1:7890\n', encoding='utf8')
        for s in sites:
            (root / f'.local/site-secrets/{s}.cookie').write_text(SECRET, encoding='utf8')
        targets = {s: {'profile': f'https://example.com/{s}', rsc.KEY_FIELD[s]: '123'} for s in sites}
        (root / '.local/site-check/m2-targets.json').write_text(json.dumps({'targets': targets}), encoding='utf8')

    def test_missing_credentials_stop_before_any_output(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            with self.assertRaises(rsc.PreparationError):
                rsc.prepare(root, ['weibo'])
            self.setup_files(root, sites=['weibo'])
            (root / '.local/site-secrets/weibo.cookie').unlink()
            with self.assertRaises(rsc.PreparationError):
                rsc.prepare(root, ['weibo'])
            (root / '.local/site-check/m2-targets.json').unlink()
            (root / '.local/site-secrets/weibo.cookie').write_text(SECRET, encoding='utf8')
            with self.assertRaises(rsc.PreparationError):
                rsc.prepare(root, ['weibo'])
            self.assertFalse((root / '.local/site-check/ledger.json').exists())

    def test_preparation_keeps_secret_out_of_output_and_reports_the_remaining_budget(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            report = rsc.prepare(root, SITES)
            self.assertEqual(report['networkRequests'], 0)
            self.assertNotIn(SECRET, json.dumps(report))
            self.assertNotIn('local-fixture-only', json.dumps(report))
            self.assertEqual(report['sites']['weibo']['remaining'], {'metadata_requests': 38, 'media_bytes': 512 * 1024 * 1024})
            self.assertTrue(report['sites']['instagram']['viaProxy'])
            self.assertTrue(report['sites']['weibo']['viaProxy'], 'DEC-035 sets the proxy globally for every site')
            self.assertEqual(report['sites']['youtube']['cookie'] if 'youtube' in report['sites'] else 'ok', 'not needed')
            for path in (root / '.local').rglob('*.json'):
                self.assertNotIn(SECRET, path.read_text(encoding='utf8'))

    def test_a_site_without_login_needs_no_cookie_file(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root, sites=[s for s in SITES if s != 'youtube'])
            targets = json.loads((root / '.local/site-check/m2-targets.json').read_text(encoding='utf8'))
            targets['targets']['youtube'] = {'profile': 'https://example.com/youtube', 'handle': '@x'}
            (root / '.local/site-check/m2-targets.json').write_text(json.dumps(targets), encoding='utf8')
            self.assertEqual(rsc.prepare(root, ['youtube'])['sites']['youtube']['cookie'], 'not needed')
            self.assertIsNone(rsc.read_cookie(root, rsc.read_env(root), 'youtube'))

    def test_duplicate_and_malformed_inputs_are_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root, sites=['weibo'])
            cookie = root / '.local/site-secrets/weibo.cookie'
            for value in ['', 'Cookie: synthetic=value', 'a=b\nc=d', 'x' * 16385, 'no-equals-sign']:
                cookie.write_text(value, encoding='utf8')
                with self.assertRaises(rsc.PreparationError):
                    rsc.prepare(root, ['weibo'])
            cookie.write_text('synthetic=value', encoding='utf8')
            self.assertEqual(rsc.prepare(root, ['weibo'])['networkRequests'], 0)
            with (root / '.env.local').open('a', encoding='utf8') as f:
                f.write('TRACE_TEST_WEIBO_COOKIE_FILE=duplicate\n')
            with self.assertRaises(rsc.PreparationError):
                rsc.prepare(root, ['weibo'])
            (root / '.env.local').write_text('TRACE_TEST_PROXY=http://user:pass@127.0.0.1:7890\n', encoding='utf8')
            with self.assertRaises(rsc.PreparationError):
                rsc.proxy_from_env(rsc.read_env(root))
            for value in ['socks5://127.0.0.1:1080', 'http://127.0.0.1', 'http://127.0.0.1:7890/path']:
                (root / '.env.local').write_text(f'TRACE_TEST_PROXY={value}\n', encoding='utf8')
                with self.assertRaises(rsc.PreparationError):
                    rsc.proxy_from_env(rsc.read_env(root))
            (root / '.env.local').write_text('TRACE_TEST_PROXY=http://127.0.0.1:7890\n', encoding='utf8')
            self.assertEqual(rsc.proxy_from_env(rsc.read_env(root)), 'http://127.0.0.1:7890')

    def test_the_ledger_carries_spending_across_runs_and_never_exceeds_the_authorization(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            ledger = rsc.Ledger(root)
            self.assertEqual(ledger.remaining(root, 'x'), {'metadata_requests': 38, 'media_bytes': 512 * 1024 * 1024})
            ledger.add('x', 30, 100)
            ledger.add('x', 5, 50)
            again = rsc.Ledger(root)
            self.assertEqual(again.used('x'), {'metadata': 35, 'mediaBytes': 150})
            self.assertEqual(again.remaining(root, 'x'), {'metadata_requests': 3, 'media_bytes': 512 * 1024 * 1024 - 150})
            again.add('x', 100, 10 ** 10)
            self.assertEqual(again.remaining(root, 'x'), {'metadata_requests': 0, 'media_bytes': 0})
            self.assertEqual(again.remaining(root, 'bilibili')['metadata_requests'], 38, 'sites are accounted separately')
            with self.assertRaises(rsc.PreparationError):
                rsc.SiteRun(root, 'x', root, root / 'run')

    def test_a_stop_condition_is_classified_and_never_retried(self):
        for text, reason in [('rate_limited: slow down', 'rate_limit'), ('auth_required', 'login'), ('permission_denied', 'unclear_permission'),
                             ('please complete the CAPTCHA', 'challenge'), ('account suspended', 'challenge'), ('request budget reached', 'budget')]:
            stop = rsc.classify(RuntimeError(text))
            self.assertIsNotNone(stop, text)
            self.assertEqual(stop.reason, reason)
        self.assertIsNone(rsc.classify(RuntimeError('invalid input: no such entry')))

    def test_receipts_fold_into_the_public_record_by_worst_outcome(self):
        data = {'sites': {'weibo': {'run': {}, 'rules': {'weibo.post.link': {'entry': 'link', 'status': 'partial', 'checks': [{'name': 'old', 'outcome': 'passed'}]},
                                                         'weibo.user.posts': {'entry': 'subscription', 'status': 'partial', 'checks': []}}}}}
        receipt = {'site': 'weibo', 'date': '2026-10-18', 'stopReason': None, 'checks': [
            {'name': 'single-link-download', 'rules': ['weibo.post.link'], 'outcome': 'passed', 'metadataRequests': 3, 'mediaBytes': 10},
            {'name': 'single-link-download', 'rules': ['weibo.post.link'], 'outcome': 'failed', 'metadataRequests': 2, 'mediaBytes': 0},
            {'name': 'account-recognition', 'rules': ['weibo.user.posts'], 'outcome': 'passed', 'metadataRequests': 1, 'mediaBytes': 0},
            {'name': 'incremental-auto-on', 'rules': ['weibo.user.posts'], 'outcome': 'not-run'}]}
        rsc.merge_receipt(data, receipt)
        link = {c['name']: c for c in data['sites']['weibo']['rules']['weibo.post.link']['checks']}
        self.assertEqual(link['single-link-download']['outcome'], 'failed')
        self.assertEqual(link['single-link-download']['requests'], 5)
        self.assertIn('old', link, 'checks from other names are kept')
        self.assertEqual(data['sites']['weibo']['rules']['weibo.user.posts']['checks'][0]['outcome'], 'passed')
        self.assertEqual(data['sites']['weibo']['run']['date'], '2026-10-18')
        receipt['checks'][0]['rules'] = ['weibo.nothing.link']
        with self.assertRaises(ValueError):
            rsc.merge_receipt(data, receipt)

    def test_a_kind_of_work_the_account_does_not_have_does_not_block_a_link_check_that_ran(self):
        def folded(*checks):
            data = {'sites': {'tiktok': {'run': {}, 'rules': {'tiktok.post.link': {'entry': 'link', 'status': 'partial', 'checks': []}}}}}
            rsc.merge_receipt(data, {'site': 'tiktok', 'date': '2026-10-18', 'stopReason': None, 'checks': [
                {'name': 'single-link-download', 'rules': ['tiktok.post.link'], **c} for c in checks]})
            return data['sites']['tiktok']['rules']['tiktok.post.link']['checks'][0]['outcome']
        video = {'outcome': 'passed', 'note': 'request succeeded, offers 8, selected 1'}
        none_image = {'outcome': 'not-run', 'note': 'no image candidate among the listed works'}
        self.assertEqual(folded(video, none_image, {**none_image, 'note': 'no text candidate among the listed works'}), 'passed')
        self.assertEqual(folded({'outcome': 'failed'}, none_image), 'failed')
        self.assertEqual(folded(video, {'outcome': 'not-run', 'note': 'site stopped earlier'}), 'not-run', 'a check that did not run for another reason still blocks')
        self.assertEqual(folded(none_image), 'not-run', 'when nothing ran, nothing passed')

    def test_a_check_cut_short_by_the_budget_is_not_completed_while_a_push_back_stays_stopped(self):
        def record(reason):
            data = {'sites': {'weibo': {'run': {}, 'rules': {'weibo.user.posts': {'entry': 'subscription', 'status': 'partial', 'checks': []}}}}}
            rsc.merge_receipt(data, {'site': 'weibo', 'date': '2026-10-18', 'stopReason': reason, 'checks': [
                {'name': 'incremental-auto-off', 'rules': ['weibo.user.posts'], 'outcome': 'stopped', 'metadataRequests': 2, 'mediaBytes': 0}]})
            return data['sites']['weibo']['rules']['weibo.user.posts']['checks'][0]['outcome']
        self.assertEqual(record('budget'), 'not-run')
        self.assertEqual(record('rate_limit'), 'stopped')

    def test_candidates_skip_pinned_live_and_already_saved_works(self):
        def item(key, kinds, **extra):
            return {'native_key': key, 'content_kinds': kinds, 'video_count': int('video' in kinds), 'image_count': int('image' in kinds),
                    'duration_ms': extra.pop('duration_ms', 30000), 'in_library': False, 'pinned': False, **extra}
        items = [item('pinned', ['video'], pinned=True), item('saved', ['video'], in_library=True), item('live', ['video', 'live']),
                 item('long', ['video'], duration_ms=3600000), item('v1', ['video']), item('mixed', ['image', 'video']),
                 item('i1', ['image']), item('t1', ['text'])]
        self.assertEqual(rsc.pick(items, 'video')['native_key'], 'v1')
        self.assertEqual(rsc.pick(items, 'image')['native_key'], 'i1')
        self.assertEqual(rsc.pick(items, 'text')['native_key'], 't1')
        self.assertIsNone(rsc.pick(items[:4], 'image'))
        by_length = [item('hour', ['video'], duration_ms=3600000), item('ten', ['video'], duration_ms=600000), item('two', ['video'], duration_ms=120000)]
        self.assertEqual([rsc.pick(by_length, 'video', rank=n)['native_key'] for n in (0, 1)], ['two', 'ten'], 'the shortest first')
        self.assertIsNone(rsc.pick(by_length, 'video', rank=2), 'an hour-long video is never chosen')
        dated = [item('old-short', ['video'], duration_ms=10000, published_at='2026-01-01T00:00:00Z'),
                 item('new-long', ['video'], duration_ms=600000, published_at='2026-10-01T00:00:00Z'),
                 item('new-short', ['video'], duration_ms=60000, published_at='2026-09-30T00:00:00Z'),
                 item('undated', ['video'], duration_ms=1000)]
        self.assertEqual(rsc.pick(dated, 'video', dated=True)['native_key'], 'old-short')
        self.assertEqual(rsc.pick(dated, 'video', dated=True, recent=2)['native_key'], 'new-short', 'the shortest of the newest two')
        self.assertEqual(rsc.pick(dated, 'video', dated=True, recent=1)['native_key'], 'new-long')
        at = rsc.datetime(2026, 10, 18, 12, 0, tzinfo=rsc.timezone.utc)
        exact = {'published_at': '2026-10-10T08:00:00Z', 'published_precision': 'source'}
        self.assertEqual(rsc.incremental_range(exact, at), {'kind': 'date_range', 'start': '2026-10-10T08:00:00Z', 'end': '2026-10-10T08:00:00Z'})
        for precision in ('approximate', 'day'):
            self.assertEqual(rsc.incremental_range({**exact, 'published_precision': precision}, at), {'kind': 'recent_days', 'days': 10}, precision)
        self.assertEqual(rsc.incremental_range({'published_at': '2026-10-18T11:00:00Z'}, at)['kind'], 'date_range', 'a list that does not say is taken at its word')
        for site in SITES:
            self.assertTrue(rsc.work_url(site, {'native_key': 'K1'}, {rsc.KEY_FIELD[site]: 'someone'}).startswith('https://'))

    def quiet_clock(self):
        """A clock that moves only when the driver sleeps, so waits cost no real time."""
        now = [0.0]
        fake = types.SimpleNamespace(sleep=lambda seconds: now.__setitem__(0, now[0] + seconds), monotonic=lambda: now[0], strftime=time.strftime, time=time.time)
        original = rsc.time
        rsc.time = fake
        self.addCleanup(setattr, rsc, 'time', original)

    def test_a_new_receipt_is_bound_to_its_package_digest_and_source_tree(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            package = root / 'dist/package-1'
            package.parent.mkdir(parents=True)
            synthetic_package(package)
            identity = rsc.package_identity(package)
            receipt = {'package': identity}
            self.assertEqual(rsc.verify_package_identity(root, receipt), [])
            self.assertEqual(rsc.verify_package_identity(root, {}), [], 'old receipts keep their original attribution')
            wrong = {'package': {**identity, 'source_tree_sha256': 'other'}}
            self.assertTrue(rsc.verify_package_identity(root, wrong))
            self.assertTrue(rsc.verify_package_identity(root, {'package': {'directory': 'x'}}))
            self.assertTrue(rsc.verify_package_identity(root, {'package': {**identity, 'directory': 'missing'}}))

            binary = package / 'trace-daemon.exe'
            binary.write_bytes(b'tampered but the checksum list is unchanged')
            errors = rsc.verify_package_identity(root, receipt)
            self.assertTrue(any('digest differs' in error for error in errors), errors)

            binary.write_bytes(b'synthetic daemon')
            (package / 'unlisted.txt').write_text('extra', encoding='utf8')
            errors = rsc.verify_package_identity(root, receipt)
            self.assertTrue(any('unlisted member' in error for error in errors), errors)

            (package / 'unlisted.txt').unlink()
            (package / 'trace-daemon.exe').unlink()
            errors = rsc.verify_package_identity(root, receipt)
            self.assertTrue(any('member is missing' in error for error in errors), errors)

    def test_an_incremental_check_creates_the_subscription_for_one_instant_pauses_it_and_stops_on_a_challenge(self):
        self.quiet_clock()
        off = IncrementalRunFixture(IncrementalCoreFixture(save_progress='deferred'))
        result = rsc.check_incremental(off, False)
        self.assertTrue(result['ok'], result)
        create = next(a for c, a in off.core.calls if c == 'subscription.create')
        self.assertEqual(create['range'], {'kind': 'date_range', 'start': '2026-10-01T08:00:00Z', 'end': '2026-10-01T08:00:00Z'})
        self.assertEqual(create['content_type'], 'video', 'the lane comes from the subscription rule')
        self.assertFalse(create['auto_download'])
        updates = [a['enabled'] for c, a in off.core.calls if c == 'subscription.update']
        self.assertEqual(updates, [False, True, False], 'paused once found, enabled for the second check, paused again')
        self.assertTrue(any(c == 'subscription.check_now' for c, _ in off.core.calls), 'the second check runs when download is off')
        self.assertEqual(sum(c == 'creator.create' for c, _ in off.core.calls), 1)
        on = IncrementalRunFixture(
            IncrementalCoreFixture(
                save_progress='complete', media_job=True, media_after=100
            )
        )
        self.assertTrue(rsc.check_incremental(on, True)['ok'])
        unresolved = IncrementalRunFixture(IncrementalCoreFixture(save_progress='deferred'))
        unresolved.account_key = None
        self.assertTrue(rsc.check_incremental(unresolved, False)['ok'], 'a run that skipped the account check recognises it first')
        bound = next(a for c, a in unresolved.core.calls if c == 'account.bind')
        self.assertEqual(bound['native_account_key'], '42')
        self.assertTrue(any(c == 'subscription.check_now' for c, _ in on.core.calls))
        self.assertEqual([a['enabled'] for c, a in on.core.calls if c == 'subscription.update'], [False, True, False])
        wrong = IncrementalRunFixture(
            IncrementalCoreFixture(save_progress='complete', media_job=True, media_after=100)
        )
        self.assertFalse(rsc.check_incremental(wrong, False)['ok'], 'a downloaded video contradicts auto-download off')
        empty = IncrementalRunFixture(
            IncrementalCoreFixture(save_progress='deferred'), used={'BV1'}
        )
        self.assertEqual(rsc.check_incremental(empty, False)['outcome'], 'not-run')
        challenged = IncrementalRunFixture(
            IncrementalCoreFixture(save_progress='deferred', fail='rate_limited')
        )
        with self.assertRaises(rsc.Stop):
            rsc.check_incremental(challenged, False)
        self.assertEqual([a['enabled'] for c, a in challenged.core.calls if c == 'subscription.update'], [False], 'a stopped check leaves nothing running')

    def test_an_incremental_check_verifies_range_detail_attempts_and_no_media_on_a_second_pass(self):
        self.quiet_clock()
        good = rsc.check_incremental(
            IncrementalRunFixture(IncrementalCoreFixture()), False
        )
        self.assertTrue(good['ok'], good)
        self.assertEqual(good['discoveredBy'][0]['subscription_id'], 'sub-1')
        self.assertTrue(good['detailRequests'])
        detail = rsc.check_incremental(
            IncrementalRunFixture(IncrementalCoreFixture(observations_after=2)), False
        )
        self.assertFalse(detail['ok'], detail)
        self.assertIn('observations grew', detail['note'])
        endpoint = rsc.check_incremental(
            IncrementalRunFixture(IncrementalCoreFixture(second_endpoint='/detail')), False
        )
        self.assertFalse(endpoint['ok'], endpoint)
        self.assertIn('endpoints unexpected', endpoint['note'])
        media = rsc.check_incremental(
            IncrementalRunFixture(IncrementalCoreFixture(media_after=99)), False
        )
        self.assertFalse(media['ok'], media)
        other_lane = rsc.check_incremental(
            IncrementalRunFixture(IncrementalCoreFixture(relation='other-sub')), False
        )
        self.assertFalse(other_lane['ok'], other_lane)
        self.assertIn('not a discovery of this subscription', other_lane['note'])
        new_job = rsc.check_incremental(
            IncrementalRunFixture(IncrementalCoreFixture(new_second_job=True)), False
        )
        self.assertFalse(new_job['ok'], new_job)
        self.assertIn('detail jobs added', new_job['note'])
        outside = rsc.check_incremental(
            IncrementalRunFixture(
                IncrementalCoreFixture(published='2026-09-01T08:00:00Z')
            ),
            False,
        )
        self.assertFalse(outside['ok'], outside)
        self.assertIn('outside the requested range', outside['note'])
        outside_relation = rsc.check_incremental(
            IncrementalRunFixture(IncrementalCoreFixture(outside=True)), False
        )
        self.assertFalse(outside_relation['ok'], outside_relation)
        self.assertIn("recorded as this subscription's discoveries", outside_relation['note'])

        auto = rsc.check_incremental(
            IncrementalRunFixture(
                IncrementalCoreFixture(media_after=100, save_progress='complete')
            ),
            True,
        )
        self.assertFalse(auto['ok'], 'media bytes without a target media job are not a download')
        auto_good = rsc.check_incremental(
            IncrementalRunFixture(
                IncrementalCoreFixture(
                    media_after=100, save_progress='complete', media_job=True
                )
            ),
            True,
        )
        self.assertTrue(auto_good['ok'], auto_good)
        auto_without_relation = rsc.check_incremental(
            IncrementalRunFixture(
                IncrementalCoreFixture(
                    media_after=100, relation=None, save_progress='complete', media_job=True
                )
            ),
            True,
        )
        self.assertFalse(auto_without_relation['ok'], auto_without_relation)
        auto_failed = rsc.check_incremental(
            IncrementalRunFixture(
                IncrementalCoreFixture(media_after=100, media_job=True)
            ),
            True,
        )
        self.assertFalse(auto_failed['ok'], auto_failed)

    def test_incremental_evidence_rejects_missing_observations_existing_outside_discovery_and_foreign_media(self):
        class MissingObservations(IncrementalCoreFixture):
            def _evidence(self, subscription_id):
                evidence = super()._evidence(subscription_id)
                for entry in evidence['entries']:
                    entry.pop('observation_count', None)
                    entry.pop('latest_observation_seq', None)
                return evidence

        missing = rsc.check_incremental(
            IncrementalRunFixture(MissingObservations()), False
        )
        self.assertFalse(missing['ok'], missing)
        self.assertIn('no observation count', missing['note'])

        class ExistingOutside(IncrementalCoreFixture):
            def __init__(self):
                super().__init__()
                self.items = [{
                    'entry_id': 'old', 'native_key': 'BV-OLD',
                    'published_at': '2026-09-01T08:00:00Z',
                    'save_progress': 'deferred',
                }]
                self.relations['old'] = ('t0', 't0')
                self.observations['old'] = 1
                self.contexts.append({
                    'entry_id': 'old', 'context_id': 'ctx-observe-old',
                    'job_id': None, 'job_generation': None, 'started_at': 't0',
                    'auth_state': 'anonymous', 'basis': 'explicit_anonymous_scope',
                })

            def _first_check(self):
                old = self.items[:]
                super()._first_check()
                self.items = old + self.items
                self.relations['old'] = ('t0', 't2')

        existing_run = IncrementalRunFixture(ExistingOutside())
        existing_run.subscription_id = 'sub-1'
        existing = rsc.check_incremental(existing_run, False)
        self.assertFalse(existing['ok'], existing)
        self.assertIn(
            "recorded as this subscription's discoveries", existing['note']
        )

        class ForeignFailedMedia(IncrementalCoreFixture):
            def _first_check(self):
                super()._first_check()
                media = next(
                    job for job in self.jobs if job['job_type'] == 'media'
                )
                media['state'] = 'failed'
                media['consumer_state'] = 'failed'

        foreign = rsc.check_incremental(
            IncrementalRunFixture(
                ForeignFailedMedia(
                    media_after=100, save_progress='complete', media_job=True
                )
            ),
            True,
        )
        self.assertFalse(foreign['ok'], foreign)
        self.assertIn('successful media execution', foreign['note'])

        class MissingMediaRequest(IncrementalCoreFixture):
            def _first_check(self):
                super()._first_check()
                self.requests = [
                    request for request in self.requests if request.get('metadata') is not False
                ]

        unowned = rsc.check_incremental(
            IncrementalRunFixture(
                MissingMediaRequest(
                    media_after=100, save_progress='complete', media_job=True
                )
            ),
            True,
        )
        self.assertFalse(unowned['ok'], unowned)
        self.assertIn('successful media execution', unowned['note'])

        auto_core = IncrementalCoreFixture(
            media_after=100,
            save_progress='complete',
            media_job=True,
            new_second_media_job=True,
        )
        auto = rsc.check_incremental(IncrementalRunFixture(auto_core), True)
        self.assertFalse(auto['ok'], auto)
        self.assertEqual(
            sum(command == 'subscription.check_now' for command, _ in auto_core.calls),
            1,
            'auto-download on must run its second check',
        )
        self.assertIn('detail jobs added', auto['note'])

    def test_a_failed_media_job_cannot_back_an_incremental_download(self):
        self.quiet_clock()
        class FailedMedia(IncrementalCoreFixture):
            def _first_check(self):
                super()._first_check()
                media = next(job for job in self.jobs if job['job_type'] == 'media')
                media['state'] = 'failed'
                media['consumer_state'] = 'failed'

        core = FailedMedia(
            media_after=100, save_progress='complete', media_job=True
        )
        result = rsc.check_incremental(IncrementalRunFixture(core), True)
        self.assertFalse(result['ok'], result)
        self.assertIn("successful media execution", result['note'])

    def test_auto_on_also_rejects_a_second_detail_job(self):
        self.quiet_clock()
        core = IncrementalCoreFixture(
            media_after=100,
            save_progress='complete',
            media_job=True,
            new_second_job=True,
        )
        result = rsc.check_incremental(IncrementalRunFixture(core), True)
        self.assertFalse(result['ok'], result)
        self.assertEqual(
            sum(command == 'subscription.check_now' for command, _ in core.calls),
            1,
            'auto-download on must run its second check',
        )
        self.assertIn('detail jobs added', result['note'])

    def test_incremental_rejects_media_activity_on_an_existing_out_of_range_work(self):
        self.quiet_clock()

        class OutsideActivity(IncrementalCoreFixture):
            def __init__(self, auto):
                super().__init__(
                    media_after=100 if auto else 0,
                    save_progress='complete' if auto else 'deferred',
                    media_job=auto,
                )
                self.items = [{
                    'entry_id': 'old', 'native_key': 'BV-OLD',
                    'published_at': '2026-09-01T08:00:00Z',
                    'save_progress': 'deferred',
                }]
                self.relations['old'] = ('t0', 't0')
                self.observations['old'] = 1
                self.contexts = [{
                    'entry_id': 'old', 'context_id': 'ctx-observe-old',
                    'job_id': None, 'job_generation': None, 'started_at': 't0',
                    'auth_state': 'anonymous',
                    'basis': 'explicit_anonymous_scope',
                }]

            def _evidence(self, subscription_id):
                evidence = copy.deepcopy(super()._evidence(subscription_id))
                for entry in evidence['entries']:
                    if (
                        entry['entry_id'] == 'old'
                        and any(
                            context['context_id'] == 'ctx-media-old'
                            for context in entry['contexts']
                        )
                    ):
                        entry['acquisitions'] = [{
                            'acquisition_id': 'acq-old',
                            'context_id': 'ctx-media-old',
                            'media_id': 'media-old-item',
                            'variant_id': 'variant-old',
                            'outcome': 'acquired',
                            'finished_at': 't3',
                            'measured_spec': {'height': 720},
                        }]
                return evidence

            def _first_check(self):
                old = copy.deepcopy(self.items)
                super()._first_check()
                self.items = old + self.items
                self.jobs.append({
                    'job_id': 'media-old', 'job_key': 'media:old',
                    'job_type': 'media', 'subject_id': 'old', 'state': 'paused',
                    'job_generation': 1, 'consumer_requested': 1,
                    'consumer_completed': 0, 'consumer_state': 'queued',
                    'updated_at': 't3',
                })
                if self.media_job:
                    self.items[0]['save_progress'] = 'complete'
                    self.contexts.append({
                        'entry_id': 'old', 'context_id': 'ctx-media-old',
                        'job_id': 'media-old', 'job_generation': 1,
                        'started_at': 't3', 'auth_state': 'anonymous',
                        'basis': 'explicit_anonymous_scope',
                    })
                    self.jobs[-1].update(
                        state='succeeded',
                        consumer_completed=1,
                        consumer_state='succeeded',
                    )
                    self.requests.append({
                        'endpoint': '/media-old', 'metadata': False, 'status': 200,
                        'started_at': 't3', 'evidence_id': 'ctx-media-old',
                    })
                    self.evidence_media['ctx-media-old'] = 75
                    self.received += 75

        for auto in (False, True):
            core = OutsideActivity(auto)
            run = IncrementalRunFixture(core)
            run.subscription_id = 'sub-1'
            result = rsc.check_incremental(run, auto)
            self.assertFalse(result['ok'], result)
            self.assertIn('out-of-range related work', result['note'])

    def test_incremental_detail_request_must_belong_to_the_observe_execution(self):
        self.quiet_clock()

        class DetailFromMedia(IncrementalCoreFixture):
            def _first_check(self):
                super()._first_check()
                self.requests[1]['evidence_id'] = 'ctx-media-e1'

        result = rsc.check_incremental(
            IncrementalRunFixture(DetailFromMedia(
                media_after=100,
                save_progress='complete',
                media_job=True,
            )),
            True,
        )
        self.assertFalse(result['ok'], result)
        self.assertIn('no detail request', result['note'])

    def test_incremental_observe_accepts_explicit_anonymous_resolution_and_rejects_failure(self):
        self.quiet_clock()

        class AnonymousFallback(IncrementalCoreFixture):
            def _first_check(self):
                super()._first_check()
                self.contexts.append({
                    'entry_id': 'e1', 'context_id': 'ctx-observe-fallback',
                    'job_id': 'detail-1', 'job_generation': 1,
                    'started_at': 't3', 'auth_state': 'anonymous',
                    'basis': 'explicit_anonymous_resolution',
                })

            def _evidence(self, subscription_id):
                evidence = copy.deepcopy(super()._evidence(subscription_id))
                for entry in evidence['entries']:
                    if entry['entry_id'] == 'e1':
                        entry['observations'][0]['context_id'] = 'ctx-observe-fallback'
                return evidence

        fallback = rsc.check_incremental(IncrementalRunFixture(AnonymousFallback()), False)
        self.assertTrue(fallback['ok'], fallback)
        self.assertEqual(
            fallback['detailRequests'][0]['evidence_id'],
            'ctx-observe-e1',
            'the request before the anonymous resolution is linked by job generation',
        )

        class FailedObserve(IncrementalCoreFixture):
            def _first_check(self):
                super()._first_check()
                self.jobs[0]['state'] = 'failed'
                self.jobs[0]['consumer_state'] = 'failed'

        failed = rsc.check_incremental(IncrementalRunFixture(FailedObserve()), False)
        self.assertFalse(failed['ok'], failed)
        self.assertIn('successful observe consumer', failed['note'])

    def test_an_undated_list_takes_the_instant_from_the_works_own_page_without_downloading_it(self):
        class Core(IncrementalCoreFixture):
            def __init__(self, with_date=True):
                super().__init__(
                    published='2026-10-18T04:00:00Z',
                    site='xiaohongshu',
                    native_key='65f0a1b2c3d4e5f607182931',
                    account_key='5a1b2c3d4e5f60718293a4b5',
                )
                self.with_date = with_date

            def call(self, command, args, timeout=None):
                if command == 'source.preview':
                    self.calls.append((command, args))
                    return (
                        {'published_at': '2026-10-18T04:00:00Z',
                         'published_precision': 'exact', 'offers': [{'id': 'i1'}]}
                        if self.with_date else {'offers': [{'id': 'i1'}]}
                    )
                return super().call(command, args, timeout)

        class Run(IncrementalRunFixture):
            site, credential_id, account_id, account_key = 'xiaohongshu', 'cred-1', None, '5a1b2c3d4e5f60718293a4b5'
            target = {'profile': 'https://www.xiaohongshu.com/user/profile/5a1b2c3d4e5f60718293a4b5', 'user_id': '5a1b2c3d4e5f60718293a4b5'}
            preview = {'items': [{'native_key': '65f0a1b2c3d4e5f607182931', 'content_kinds': ['video'], 'video_count': 1, 'image_count': 0,
                                  'duration_ms': 30000, 'in_library': False, 'pinned': False, 'published_at': None}]}

        self.quiet_clock()
        run = Run(Core())
        result = rsc.check_incremental(run, False)
        self.assertTrue(result['ok'], result)
        self.assertIn('date from the work', result['note'])
        create = next(a for c, a in run.core.calls if c == 'subscription.create')
        self.assertEqual(create['range'], {'kind': 'date_range', 'start': '2026-10-18T04:00:00Z', 'end': '2026-10-18T04:00:00Z'})
        self.assertFalse(any(c == 'source.confirm' for c, _ in run.core.calls), 'the date is read without selecting media')
        self.assertEqual(sum(c == 'source.preview' for c, _ in run.core.calls), 1)
        undated = Run(Core(with_date=False))
        self.assertEqual(rsc.check_incremental(undated, False)['outcome'], 'not-run')
        self.assertFalse(any(c == 'subscription.create' for c, _ in undated.core.calls))

    def test_a_whole_run_lists_the_preview_lane_and_spends_in_the_order_that_secures_the_rules_first(self):
        listing = {'items': [
            {'native_key': 'V1', 'content_kinds': ['video'], 'video_count': 1, 'image_count': 0, 'duration_ms': 30000, 'in_library': False,
             'pinned': False, 'published_at': '2026-10-01T08:00:00Z'},
            {'native_key': 'V2', 'content_kinds': ['video'], 'video_count': 1, 'image_count': 0, 'duration_ms': 30000, 'in_library': False,
             'pinned': False, 'published_at': '2026-10-02T08:00:00Z'},
            {'native_key': 'V3', 'content_kinds': ['video'], 'video_count': 1, 'image_count': 0, 'duration_ms': 30000, 'in_library': False,
             'pinned': False, 'published_at': '2026-10-03T08:00:00Z'},
            {'native_key': 'V4', 'content_kinds': ['video'], 'video_count': 1, 'image_count': 0, 'duration_ms': 30000, 'in_library': False,
             'pinned': False, 'published_at': '2026-10-04T08:00:00Z'},
            {'native_key': 'I4', 'content_kinds': ['image'], 'video_count': 0, 'image_count': 4, 'in_library': False, 'pinned': False},
            {'native_key': 'I2', 'content_kinds': ['image'], 'video_count': 0, 'image_count': 2, 'in_library': False, 'pinned': False},
            {'native_key': 'T1', 'content_kinds': ['text'], 'video_count': 0, 'image_count': 0, 'in_library': False, 'pinned': False}],
            'preview_id': 'pv1', 'pages_loaded': 1, 'has_more': True, 'limited': False}
        seen: list[tuple[str, dict]] = []

        class Core:
            def __init__(self, daemon, profile, env=None, client=None):
                self.entries, self.requests, self.media = [], [], 0
                self.jobs, self.discoveries = [], {}
                self.relations, self.contexts, self.acquisitions = {}, [], []
                self.evidence_media = {}

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

            def call(self, command, args, timeout=None):
                seen.append((command, args))
                if command == 'credential.import':
                    return {'credential_id': 'cred-1'}
                if command == 'network.set':
                    return {}
                if command == 'credential.list':
                    return {'items': [{'credential_id': 'cred-1', 'status': 'valid', 'capability': {'login_proven': True}}]}
                if command == 'daemon.status':
                    return {'acquisition_budget': {'metadata_request_count': len(self.requests), 'media_received_bytes': self.media,
                                                   'stopped': False, 'requests': self.requests,
                                                   'evidence_media_bytes': self.evidence_media}}
                if command == 'archive.subscription.evidence':
                    subscription = args['subscription_id']
                    entries = []
                    for entry in self.entries:
                        entry_id = entry['entry_id']
                        if self.discoveries.get(entry_id) != subscription:
                            continue
                        contexts = [
                            context for context in self.contexts if context['entry_id'] == entry_id
                        ]
                        context_ids = {context['context_id'] for context in contexts}
                        entries.append({
                            **{key: value for key, value in entry.items() if key != 'save_progress'},
                            'relation': self.relations[entry_id],
                            'discovered_by_subscriptions': [
                                {
                                    'subscription_id': subscription,
                                    'first_discovered_at': self.relations[entry_id][
                                        'first_discovered_at'
                                    ],
                                    'last_discovered_at': self.relations[entry_id][
                                        'last_discovered_at'
                                    ],
                                }
                            ],
                            'observation_count': 1,
                            'latest_observation_seq': 1,
                            'observations': [{
                                'seq': 1, 'access_state': 'available', 'observed_at': 't3',
                                'context_id': f'ctx-observe-{entry_id}',
                            }],
                            'jobs': [
                                job for job in self.jobs
                                if job.get('subject_id') == entry_id
                            ],
                            'contexts': [
                                {key: value for key, value in context.items() if key != 'entry_id'}
                                for context in contexts
                            ],
                            'acquisitions': [
                                acquisition for acquisition in self.acquisitions
                                if acquisition['context_id'] in context_ids
                            ],
                        })
                    return {
                        'subscription_id': subscription,
                        'entries': entries,
                        'requests': [
                            {**request, 'method': request.get('method', 'GET')}
                            for request in self.requests if request.get('evidence_id')
                        ],
                        'evidence_media_bytes': {
                            key: value for key, value in self.evidence_media.items()
                            if any(context['context_id'] == key for context in self.contexts)
                        },
                        'budget_available': True,
                        'truncated': False,
                    }
                if command == 'archive.entry.get':
                    entry = args['entry_id']
                    return {'entry': {'entry_id': entry},
                            'discovered_by_subscriptions': (
                                [{
                                    'subscription_id': self.discoveries[entry],
                                    'first_discovered_at': 't1',
                                    'last_discovered_at': 't1',
                                }]
                                if entry in self.discoveries
                                else []
                            ),
                            'observation_count': 1 if entry in self.discoveries else 0,
                            'latest_observation_seq': 1 if entry in self.discoveries else 0}
                if command == 'account.resolve':
                    return {'native_account_key': '123', 'display_name': 'someone'}
                if command == 'source.profile.preview':
                    return listing if args['preview_id'] is None else {**listing, 'pages_loaded': 2, 'has_more': False}
                if command == 'source.profile.download':
                    folder = Path(args['target']['dir'])
                    (folder / 'work.bin').write_bytes(b'x')
                    return {'delivery_id': 'd1', 'accepted': len(args['native_keys']), 'already_in_library': 0, 'skipped': []}
                if command == 'source.deliveries':
                    return {'items': [{'state': 'done', 'items': [{'state': 'exported'}]}]}
                if command == 'source.preview':
                    return {'preview_id': 'p', 'offers': [{'id': 'o1', 'role': 'video', 'required': True, 'spec': {'height': 720}}]}
                if command == 'source.confirm':
                    return {'request_id': 'r1'}
                if command == 'source.list':
                    return {'items': [{'request_id': 'r1', 'state': 'succeeded', 'tasks': []}]}
                if command == 'job.list':
                    return {'items': [{'job_id': f'deep-{n}', 'job_key': f'scan:sub-{n}:deep', 'job_type': 'scan', 'state': 'queued'} for n in (1, 2)]
                            + [{'job_id': 'hist-1', 'job_key': 'scan:sub-1:history', 'job_type': 'scan', 'state': 'queued'}]
                            + self.jobs}
                if command == 'site.catalog':
                    return {'sites': [{'id': 'weibo', 'rules': [{'declaration': {'id': 'weibo.user.posts', 'lane': 'post'}}]}]}
                if command == 'creator.create':
                    return {'creator_id': 'cre-1'}
                if command == 'account.bind':
                    return {'account_id': 'acct-1'}
                if command == 'subscription.create':
                    key, stamp = chosen_by_range(args['range'])
                    entry = f'e{len(self.entries) + 1}'
                    self.entries.append({'entry_id': entry, 'native_key': key, 'published_at': stamp, 'save_progress': 'complete' if args['auto_download'] else 'deferred'})
                    sub = f'sub-{len(self.entries)}'
                    self.discoveries[entry] = sub
                    self.requests.extend([{'endpoint': '/list', 'metadata': True, 'status': 200, 'started_at': 't1'},
                                          {'endpoint': '/detail', 'metadata': True, 'status': 200, 'started_at': 't2',
                                           'evidence_id': f'ctx-observe-{entry}'}])
                    self.jobs.append({'job_id': f'observe-{entry}', 'job_key': f'observe:{entry}', 'job_type': 'observe',
                                      'subject_id': entry, 'state': 'succeeded', 'updated_at': 't3',
                                      'job_generation': 1, 'consumer_requested': 1, 'consumer_completed': 1,
                                      'consumer_state': 'succeeded'})
                    self.relations[entry] = {
                        'first_discovered_at': 't1', 'last_discovered_at': 't1',
                    }
                    self.contexts.append({
                        'entry_id': entry, 'context_id': f'ctx-observe-{entry}',
                        'job_id': f'observe-{entry}', 'job_generation': 1,
                        'started_at': 't2', 'auth_state': 'anonymous',
                        'basis': 'explicit_anonymous_scope',
                    })
                    if args['auto_download']:
                        self.media += 100
                        self.evidence_media[f'ctx-media-{entry}'] = 100
                        self.jobs.append({'job_id': f'media-{entry}', 'job_key': f'media:{entry}', 'job_type': 'media',
                                          'subject_id': entry, 'state': 'succeeded', 'updated_at': 't3',
                                          'job_generation': 1, 'consumer_requested': 1, 'consumer_completed': 1,
                                          'consumer_state': 'succeeded'})
                        self.contexts.append({
                            'entry_id': entry, 'context_id': f'ctx-media-{entry}',
                            'job_id': f'media-{entry}', 'job_generation': 1,
                            'started_at': 't3', 'auth_state': 'anonymous',
                            'basis': 'explicit_anonymous_scope',
                        })
                        self.acquisitions.append({
                            'acquisition_id': f'acq-{entry}',
                            'context_id': f'ctx-media-{entry}',
                            'media_id': f'm-{entry}', 'variant_id': f'v-{entry}',
                            'outcome': 'acquired', 'finished_at': 't3',
                            'measured_spec': {'height': 720},
                        })
                        self.requests.append({
                            'endpoint': '<page-or-redirect>', 'metadata': False,
                            'status': 200, 'started_at': 't3',
                            'evidence_id': f'ctx-media-{entry}',
                        })
                    return {'subscription_id': sub}
                if command == 'subscription.update' and 'range' in args:
                    key, stamp = chosen_by_range(args['range'])
                    entry = f'e{len(self.entries) + 1}'
                    self.entries.append({'entry_id': entry, 'native_key': key, 'published_at': stamp, 'save_progress': 'complete' if args['auto_download'] else 'deferred'})
                    sub = args['subscription_id']
                    self.discoveries[entry] = sub
                    self.requests.extend([{'endpoint': '/list', 'metadata': True, 'status': 200, 'started_at': 't4'},
                                          {'endpoint': '/detail', 'metadata': True, 'status': 200, 'started_at': 't5',
                                           'evidence_id': f'ctx-observe-{entry}'}])
                    self.jobs.append({'job_id': f'observe-{entry}', 'job_key': f'observe:{entry}', 'job_type': 'observe',
                                      'subject_id': entry, 'state': 'succeeded', 'updated_at': 't6',
                                      'job_generation': 1, 'consumer_requested': 1, 'consumer_completed': 1,
                                      'consumer_state': 'succeeded'})
                    self.relations[entry] = {
                        'first_discovered_at': 't4', 'last_discovered_at': 't4',
                    }
                    self.contexts.append({
                        'entry_id': entry, 'context_id': f'ctx-observe-{entry}',
                        'job_id': f'observe-{entry}', 'job_generation': 1,
                        'started_at': 't5', 'auth_state': 'anonymous',
                        'basis': 'explicit_anonymous_scope',
                    })
                    if args['auto_download']:
                        self.media += 100
                        self.evidence_media[f'ctx-media-{entry}'] = 100
                        self.jobs.append({'job_id': f'media-{entry}', 'job_key': f'media:{entry}', 'job_type': 'media',
                                          'subject_id': entry, 'state': 'succeeded', 'updated_at': 't6',
                                          'job_generation': 1, 'consumer_requested': 1, 'consumer_completed': 1,
                                          'consumer_state': 'succeeded'})
                        self.contexts.append({
                            'entry_id': entry, 'context_id': f'ctx-media-{entry}',
                            'job_id': f'media-{entry}', 'job_generation': 1,
                            'started_at': 't6', 'auth_state': 'anonymous',
                            'basis': 'explicit_anonymous_scope',
                        })
                        self.acquisitions.append({
                            'acquisition_id': f'acq-{entry}',
                            'context_id': f'ctx-media-{entry}',
                            'media_id': f'm-{entry}', 'variant_id': f'v-{entry}',
                            'outcome': 'acquired', 'finished_at': 't6',
                            'measured_spec': {'height': 720},
                        })
                        self.requests.append({
                            'endpoint': '<page-or-redirect>', 'metadata': False,
                            'status': 200, 'started_at': 't6',
                            'evidence_id': f'ctx-media-{entry}',
                        })
                    return {}
                if command == 'subscription.check_now':
                    self.requests.append({'endpoint': '/list', 'metadata': True, 'status': 200, 'started_at': 't7'})
                    return {}
                if command == 'archive.timeline':
                    return {'items': self.entries}
                return {}

        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            package = root / 'package'
            synthetic_package(package)
            self.quiet_clock()
            real = sys.modules.get('core_client')
            sys.modules['core_client'] = types.SimpleNamespace(Core=Core, CoreError=RuntimeError)
            try:
                receipt = rsc.run_site(root, 'weibo', package, list(rsc.STEPS), 'run-dry')
            finally:
                if real is None:
                    del sys.modules['core_client']
                else:
                    sys.modules['core_client'] = real
        names = [c['name'] for c in receipt['checks']]
        self.assertEqual(names, ['account-recognition', 'list-two-pages', 'single-link-download', 'preview-select-download',
                                 'incremental-auto-off', 'incremental-auto-on', 'single-link-download', 'single-link-download'])
        self.assertEqual([c['outcome'] for c in receipt['checks']], ['passed'] * 8, receipt['checks'])
        lists = [a for c, a in seen if c == 'source.profile.preview']
        self.assertTrue(all(a['rule_id'] == 'weibo.user.preview' for a in lists if a['preview_id'] is None), 'the list is the rule being checked, not the first one')
        download = next(a for c, a in seen if c == 'source.profile.download')
        self.assertEqual(len(download['native_keys']), 2, 'a video and one picture work')
        self.assertEqual(sorted(download['native_keys']), ['I2', 'V2'], 'a video not used before and the picture work with the fewest pictures')
        self.assertEqual(sum(c == 'subscription.create' for c, _ in seen), 1, 'an account has one subscription per lane: the second check updates it')
        moved = next(a for c, a in seen if c == 'subscription.update' and 'range' in a)
        self.assertTrue(moved['auto_download'] and moved['enabled'])
        subscription = next(a for c, a in seen if c == 'subscription.create')
        self.assertEqual([a['job_id'] for c, a in seen if c == 'job.cancel'], ['deep-1', 'deep-1'], 'each time a check turns the subscription on, only its full-verification lane is dropped')
        self.assertEqual(subscription['content_type'], 'post', 'the lane of the weibo subscription rule')
        self.assertEqual(receipt['site'], 'weibo')
        self.assertNotIn(SECRET, json.dumps(receipt))

    def test_a_site_whose_account_cannot_be_recognised_spends_nothing_more(self):
        seen: list[str] = []

        class Failure(RuntimeError):
            def __init__(self, command, error):
                super().__init__(f'{command}: {error}')
                self.error = error

        class Core:
            def __init__(self, daemon, profile, env=None, client=None):
                self.env = env

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

            def call(self, command, args, timeout=None):
                seen.append(command)
                if command == 'credential.import':
                    return {'credential_id': 'cred-1'}
                if command == 'network.set':
                    return {}
                if command == 'credential.list':
                    return {'items': [{'credential_id': 'cred-1', 'status': 'valid', 'capability': {'login_proven': True}}]}
                if command == 'daemon.status':
                    return {'acquisition_budget': {'metadata_request_count': 0, 'media_received_bytes': 0, 'stopped': False, 'requests': []}}
                if command == 'account.resolve':
                    raise Failure(command, {'code': 'invalid_input', 'details': {}})
                raise AssertionError(f'{command} must not be called after the account failed')

        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            package = root / 'package'
            synthetic_package(package)
            self.quiet_clock()
            real = sys.modules.get('core_client')
            sys.modules['core_client'] = types.SimpleNamespace(Core=Core, CoreError=Failure)
            try:
                receipt = rsc.run_site(root, 'weibo', package, list(rsc.STEPS), 'run-dry')
            finally:
                if real is None:
                    del sys.modules['core_client']
                else:
                    sys.modules['core_client'] = real
        outcomes = [c['outcome'] for c in receipt['checks']]
        self.assertEqual(outcomes, ['failed'] + ['not-run'] * 7, receipt['checks'])
        self.assertEqual(len([c for c in seen if c == 'account.resolve']), 1)
        self.assertNotIn('source.profile.preview', seen)

    def test_a_refusal_that_stops_the_site_inside_core_ends_the_run_for_that_site(self):
        halted = [False]

        class Core:
            def __init__(self, daemon, profile, env=None, client=None):
                pass

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

            def call(self, command, args, timeout=None):
                if command == 'credential.import':
                    return {'credential_id': 'cred-1'}
                if command == 'network.set':
                    return {}
                if command == 'credential.list':
                    return {'items': [{'credential_id': 'cred-1', 'status': 'valid', 'capability': {'login_proven': True}}]}
                if command == 'daemon.status':
                    last = {'metadata': False, 'method': 'GET', 'status': 403} if halted[0] else {'metadata': True, 'method': 'GET', 'status': 200}
                    return {'acquisition_budget': {'metadata_request_count': 3, 'media_received_bytes': 0, 'stopped': halted[0], 'requests': [last]}}
                if command == 'account.resolve':
                    return {'native_account_key': '123', 'display_name': 'someone'}
                if command == 'source.profile.preview':
                    return {'items': [{'native_key': 'V1', 'content_kinds': ['video'], 'video_count': 1, 'image_count': 0, 'duration_ms': 30000,
                                       'in_library': False, 'pinned': False, 'published_at': '2026-10-01T08:00:00Z'}],
                            'preview_id': 'pv1', 'pages_loaded': 2, 'has_more': False, 'limited': False}
                if command == 'source.profile.download':
                    halted[0] = True
                    return {'delivery_id': 'd1', 'accepted': 1, 'already_in_library': 0, 'skipped': []}
                if command == 'source.deliveries':
                    return {'items': [{'state': 'done', 'items': [{'state': 'failed'}]}]}
                if command == 'job.list':
                    return {'items': []}
                raise AssertionError(f'{command} must not be called once Core has stopped the site')

        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            package = root / 'package'
            synthetic_package(package)
            self.quiet_clock()
            real = sys.modules.get('core_client')
            sys.modules['core_client'] = types.SimpleNamespace(Core=Core, CoreError=RuntimeError)
            try:
                receipt = rsc.run_site(root, 'weibo', package, ['account', 'list', 'preview-download', 'single-video'], 'run-dry')
            finally:
                if real is None:
                    del sys.modules['core_client']
                else:
                    sys.modules['core_client'] = real
        self.assertEqual([c['outcome'] for c in receipt['checks']], ['passed', 'passed', 'stopped', 'not-run'], receipt['checks'])
        self.assertEqual(receipt['stopReason'], 'refused')
        self.assertIn('HTTP 403', receipt['stopDetail'])

    def test_a_site_that_does_not_see_the_login_is_stopped_before_any_check(self):
        seen: list[str] = []

        class Core:
            def __init__(self, daemon, profile, env=None, client=None):
                pass

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

            def call(self, command, args, timeout=None):
                seen.append(command)
                if command == 'credential.import':
                    return {'credential_id': 'cred-1'}
                if command == 'network.set':
                    return {}
                if command == 'credential.list':
                    return {'items': [{'credential_id': 'cred-1', 'status': 'invalid', 'capability': {'login_proven': False, 'source': 'home.viewer'}}]}
                if command == 'daemon.status':
                    return {'acquisition_budget': {'metadata_request_count': 1, 'media_received_bytes': 0, 'stopped': False, 'requests': []}}
                raise AssertionError(f'{command} must not be called when the login is not seen')

        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            package = root / 'package'
            synthetic_package(package)
            self.quiet_clock()
            real = sys.modules.get('core_client')
            sys.modules['core_client'] = types.SimpleNamespace(Core=Core, CoreError=RuntimeError)
            try:
                receipt = rsc.run_site(root, 'weibo', package, list(rsc.STEPS), 'run-dry')
            finally:
                if real is None:
                    del sys.modules['core_client']
                else:
                    sys.modules['core_client'] = real
        self.assertEqual(receipt['stopReason'], 'login')
        self.assertIn('home.viewer', receipt['stopDetail'])
        self.assertEqual(receipt['checks'][0]['name'], 'login-probe')
        self.assertTrue(all(c['outcome'] in ('stopped', 'not-run') for c in receipt['checks']))
        self.assertEqual(
            sorted(set(seen)),
            ['credential.import', 'credential.list', 'daemon.status', 'network.set'],
        )

    def test_the_offers_chosen_for_a_download_are_one_per_resource(self):
        offers = [{'id': 'v1080', 'attachment_id': 'v', 'role': 'video', 'required': True, 'spec': {'height': 1080}},
                  {'id': 'v720', 'attachment_id': 'v', 'role': 'video', 'required': True, 'spec': {'height': 720}},
                  {'id': 'v480', 'attachment_id': 'v', 'role': 'video', 'required': True, 'spec': {'height': 480}},
                  {'id': 'a1', 'attachment_id': 'a', 'role': 'audio', 'required': True},
                  {'id': 'a2', 'attachment_id': 'a', 'role': 'audio', 'required': True},
                  {'id': 'cover', 'attachment_id': 'c', 'role': 'cover', 'required': False},
                  {'id': 'img', 'attachment_id': 'i', 'role': 'image', 'required': True}]
        self.assertEqual(rsc.choose_offers(offers), ['v720', 'a1', 'img'])
        self.assertEqual(rsc.choose_offers([{'id': 'only', 'role': 'video', 'required': True, 'spec': {'height': 2160}}]), ['only'])
        self.assertEqual(rsc.choose_offers([{'id': 'x', 'role': 'cover', 'required': False}]), ['x'])
        pictures = [{'id': 'p1-best', 'attachment_id': 'p1', 'role': 'image', 'required': True},
                    {'id': 'p1-other', 'attachment_id': 'p1', 'role': 'image', 'required': True},
                    {'id': 'p2-best', 'attachment_id': 'p2', 'role': 'image', 'required': True},
                    {'id': 'p2-other', 'attachment_id': 'p2', 'role': 'image', 'required': True}]
        self.assertEqual(rsc.choose_offers(pictures), ['p1-best', 'p2-best'], 'every picture once, in its best form')

    def test_a_site_whose_posts_are_a_lane_of_their_own_loads_it_once_for_pictures_and_texts(self):
        calls: list[tuple[str, dict]] = []

        class Core:
            def call(self, command, args, timeout=None):
                calls.append((command, args))
                if command == 'source.profile.preview':
                    return {'items': [{'native_key': 'P1', 'content_kinds': ['image'], 'video_count': 0, 'image_count': 1,
                                       'in_library': False, 'pinned': False},
                                      {'native_key': 'P2', 'content_kinds': ['text'], 'video_count': 0, 'image_count': 0,
                                       'in_library': False, 'pinned': False}]}
                if command == 'source.preview':
                    return {'preview_id': 'pv', 'offers': [{'id': 'o1', 'attachment_id': 'a', 'role': 'image', 'required': True}]}
                if command == 'source.confirm':
                    return {'request_id': 'r1'}
                if command == 'source.list':
                    return {'items': [{'request_id': 'r1', 'state': 'succeeded', 'tasks': []}]}
                raise AssertionError(command)

        run = types.SimpleNamespace(site='youtube', target={'profile': 'https://example.com/y', 'handle': 'h'}, credential_id=None, core=Core(),
                                    used_keys=set(), posts=None,
                                    preview={'items': [{'native_key': 'V1', 'content_kinds': ['video'], 'video_count': 1, 'image_count': 0,
                                                        'duration_ms': 1000, 'in_library': False, 'pinned': False}]})
        self.assertTrue(rsc.check_single_link(run, 'image')['ok'])
        self.assertTrue(rsc.check_single_link(run, 'text')['ok'])
        lanes = [a for c, a in calls if c == 'source.profile.preview']
        self.assertEqual([a['rule_id'] for a in lanes], ['youtube.channel.preview_posts'], 'the lane is loaded once')
        urls = [a['url'] for c, a in calls if c == 'source.preview']
        self.assertEqual(urls, ['https://www.youtube.com/post/P1', 'https://www.youtube.com/post/P2'])

    def test_a_video_or_picture_link_that_offers_nothing_downloaded_nothing_and_is_not_a_pass(self):
        class Core:
            def call(self, command, args, timeout=None):
                if command == 'source.preview':
                    return {'preview_id': 'pv', 'offers': []}
                if command == 'source.confirm':
                    return {'request_id': 'r1'}
                if command == 'source.list':
                    return {'items': [{'request_id': 'r1', 'state': 'succeeded', 'tasks': []}]}
                raise AssertionError(command)

        items = [{'native_key': 'V1', 'content_kinds': ['video'], 'video_count': 1, 'image_count': 0, 'duration_ms': 1000, 'in_library': False, 'pinned': False},
                 {'native_key': 'P1', 'content_kinds': ['image'], 'video_count': 0, 'image_count': 1, 'in_library': False, 'pinned': False},
                 {'native_key': 'T1', 'content_kinds': ['text'], 'video_count': 0, 'image_count': 0, 'in_library': False, 'pinned': False}]
        run = types.SimpleNamespace(site='weibo', target={'profile': 'https://example.com/w', 'handle': 'h'}, credential_id=None, core=Core(),
                                    used_keys=set(), posts=None, preview={'items': items})
        self.assertFalse(rsc.check_single_link(run, 'video')['ok'])
        self.assertFalse(rsc.check_single_link(run, 'image')['ok'])
        self.assertTrue(rsc.check_single_link(run, 'text')['ok'], 'a text post may have nothing to download')

    def test_the_record_takes_the_newest_receipt_last_whatever_the_labels_sort_like(self):
        import os
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            self.setup_files(root)
            record_path = root / rule_evidence.FILE
            record_path.parent.mkdir(parents=True)
            record_path.write_text(json.dumps({'sites': {'weibo': {'run': {}, 'rules': {'weibo.post.link': {'entry': 'link', 'status': 'partial', 'checks': []}}}}}), encoding='utf8')
            for label, outcome, when in (('run-m2-r10', 'passed', 2000), ('run-m2-r9', 'failed', 1000)):
                folder_ = root / rsc.LOCAL / label / 'weibo'
                folder_.mkdir(parents=True)
                receipt = folder_ / 'receipt.json'
                receipt.write_text(json.dumps({'site': 'weibo', 'date': '2026-10-17', 'stopReason': None, 'checks': [
                    {'name': 'single-link-download', 'rules': ['weibo.post.link'], 'outcome': outcome, 'metadataRequests': 1, 'mediaBytes': 0}]}), encoding='utf8')
                os.utime(receipt, (when, when))
            rsc.record(root, ['weibo'])
            data = json.loads(record_path.read_text(encoding='utf8'))
        checks = data['sites']['weibo']['rules']['weibo.post.link']['checks']
        self.assertEqual([(c['name'], c['outcome']) for c in checks], [('single-link-download', 'passed')])

    def test_the_public_record_names_only_rules_the_run_can_speak_for(self):
        import evidence
        declared = {rule for site in rsc.RULES.values() for kind in site.values() for rule in kind}
        data = json.loads((rsc.ROOT / rule_evidence.FILE).read_text(encoding='utf8'))
        recorded = {rid for site in data['sites'].values() for rid in site['rules']}
        self.assertTrue(declared <= recorded, sorted(declared - recorded))
        self.assertEqual(sorted(rsc.RULES), sorted(evidence.adapter_sites()))


if __name__ == '__main__':
    unittest.main()
