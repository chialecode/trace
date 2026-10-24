import hashlib
import os
import subprocess
import copy
import json
import sys
import tempfile
import unittest
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from evidence import validate,validate_package,validate_rule_evidence,sha,parse_cases,contained,inventory,requirement_coverage,inputs,package_layout,ROOT
from benchmark_metrics import derive

SITES=['alpha','beta']
TOOL_BYTES={'yt-dlp.exe':b'pinned yt-dlp','deno.exe':b'pinned deno','yt-dlp-LICENSE.txt':b'yt-dlp licence','deno-LICENSE.md':b'deno licence'}
digest=lambda name:hashlib.sha256(TOOL_BYTES[name]).hexdigest()
LOCK={'yt_dlp':{'sha256':digest('yt-dlp.exe'),'notices':[{'name':'yt-dlp-LICENSE.txt','sha256':digest('yt-dlp-LICENSE.txt')}]},'deno':{'sha256':hashlib.sha256(b'the pinned archive').hexdigest(),'member_sha256':digest('deno.exe'),'notices':[{'name':'deno-LICENSE.md','sha256':digest('deno-LICENSE.md')}]}}
PRIOR_TABLE=''.join(f'| {name} | 1 | 已取到 |\n' for name in ['微博','小红书','Instagram','X','TikTok','YouTube','哔哩哔哩','抖音'])
USER_ACTIONS='<!-- ACT-007-AUTHORIZED-BUDGETS '+json.dumps({s:{'metadata_requests':60,'media_bytes':10**6} for s in SITES})+' -->\n\n**2026-10-16 A 的一次登录与目标探测**\n\n| 站点 | 已用请求 | 结果 |\n| --- | --- | --- |\n'+PRIOR_TABLE+'\n格式检查也通过。\n'
RULES={
    'alpha':{'run':{'date':'2026-10-18','metadataRequests':10,'mediaBytes':100,'stoppedBy':None},'rules':{
        'alpha.user.posts':{'entry':'subscription','status':'implemented','verifiedAt':'2026-10-18','scope':'two pages of one account','checks':[{'name':'account-recognition','outcome':'passed'},{'name':'list-two-pages','outcome':'passed'},{'name':'incremental-auto-off','outcome':'passed'},{'name':'incremental-auto-on','outcome':'passed'}]},
        'alpha.post.link':{'entry':'link','status':'partial','note':'only photos were checked','checks':[{'name':'single-link-download','outcome':'not-run'}]}}},
    'beta':{'run':{'date':'2026-10-18','metadataRequests':0,'mediaBytes':0,'stoppedBy':'rate_limit'},'rules':{
        'beta.user.preview':{'entry':'preview','status':'unimplemented','note':'the site answered with a limit; stopped','checks':[{'name':'list-two-pages','outcome':'stopped'}]}}}}


class StageEvidenceTests(unittest.TestCase):
    def test_verbose_receipts_preserve_names_and_executable_identity(self):
        text='''     Running unittests src/lib.rs (target\\debug\\deps\\trace_core-012abc.exe)
test nested::same_name ... ok
     Running unittests src/lib.rs (target/debug/deps/trace_contracts-345def.exe)
test nested::same_name ... FAILED
test_required (test_module.Examples.test_required) ... ok
 ✓ src/components/__tests__/m1.spec.ts > c08_ui_case 21ms
 × src/components/__tests__/m1.spec.ts > c08_bad_case 3ms
'''
        cases=parse_cases(text)
        self.assertEqual(len(cases),5)
        self.assertNotEqual(cases[0]['id'],cases[1]['id'])
        self.assertEqual(cases[0]['id'],'rust:trace_core:nested::same_name')
        self.assertEqual(cases[1]['status'],'failed')
        self.assertEqual(cases[3]['name'],'c08_ui_case')
        self.assertEqual(cases[4]['status'],'failed')
        nested=parse_cases(''' ✓ src/views/__tests__/home.spec.ts > home > events > c08_nested 21ms
 ✓ src/views/__tests__/home.spec.ts > home > other events > c08_nested 22ms
 × src/views/__tests__/home.spec.ts > home > c08_failed 23ms
''')
        self.assertEqual(len(nested),3)
        self.assertEqual([case['name'] for case in nested],['c08_nested','c08_nested','c08_failed'])
        self.assertNotEqual(nested[0]['id'],nested[1]['id'])
        self.assertEqual(nested[0]['id'],'vitest:src/views/__tests__/home.spec.ts:home > events > c08_nested')
        self.assertEqual(nested[2]['status'],'failed')

    def fixture(self,root):
        for site in SITES:
            (root/'adapters'/site).mkdir(parents=True,exist_ok=True)
            (root/'adapters'/site/'Cargo.toml').write_text(f'[package]\nname = "trace-adapter-{site}"\n')
        (root/'media-tools.lock.json').write_text(json.dumps(LOCK))
        layout=package_layout(root)
        for name in [*layout['components'],*layout['media'],*layout['tools'],*layout['legal']]:
            p=root/'package'/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(b'isolated gate fixture '+name.encode())
        # The tool receipt binds the pinned digests of the lock to the shipped files.
        for name,data in TOOL_BYTES.items():(root/'package/tools'/name).write_bytes(data)
        (root/'package/tools/verified.json').write_text(json.dumps({'lock':LOCK,'files':{name:digest(name) for name in TOOL_BYTES}}))
        (root/'raw.log').write_text('test required ... ok'+chr(10));(root/'other.log').write_text('checked'+chr(10))
        source={'code':'a','tests':'b','lock':'c'}
        (root/'docs/delivery').mkdir(parents=True);(root/'docs/evidence').mkdir(parents=True);(root/'crates/domain/src').mkdir(parents=True)
        (root/'docs/delivery/stage-plan.md').write_text('TRACE-M2-01 r1')
        (root/'crates/domain/src/lib.rs').write_text('pub const SCHEMA_VERSION: i64 = 6;')
        mapping={'currentPlan':'TRACE-M2-01 r1','requirements':[{'id':f'FR-fixture-{i:03}','current':{'plan':'TRACE-M2-01 r1','applicable':i<68,'caseGroups':['C01'] if i<68 else [],'testCases':['required'] if i<68 else [],'implementation':['code'] if i<68 else []}} for i in range(87)]}
        (root/'docs/delivery/requirements-map.json').write_text(json.dumps(mapping))
        (root/'media-backend.lock.json').write_text('{}')
        (root/'USER-ACTIONS.md').write_text(USER_ACTIONS,encoding='utf8')
        (root/'docs/evidence/rule-evidence.json').write_text(json.dumps({'schemaVersion':1,'plan':'TRACE-M2-01 r1','sites':RULES}),encoding='utf8')
        components=layout['components']
        manifest={'sourceInputs':source,'source_files_sha256':source,'source_tree_sha256':hashlib.sha256(json.dumps(source,sort_keys=True,separators=(',',':')).encode()).hexdigest(),
            'stage':'TRACE-M2-01 r1','productAcceptance':'not-run','schema_version':6,'components':components,'built_binaries':{n:sha(root/'package'/n) for n in components},
            'build_commands':[{'argv':[cmd,'trace-desktop/custom-protocol'],'exitCode':0,'sourceInputs':source,'sourceUnchanged':True,'log':'raw.log','logSha256':sha(root/'raw.log')} for cmd in ['cargo','corepack.cmd']]}
        (root/'package/manifest.json').write_text(json.dumps(manifest))
        (root/'package/profile.example.json').write_text(json.dumps({'test_allowed_hosts':[],'extra_media_hosts':[]}))
        (root/'package/media/verified.json').write_text(json.dumps({'lock':{},'files':{n:sha(root/'package/media'/n) for n in ['ffmpeg.exe','ffprobe.exe','ffplay.exe']}}))
        self.package_sums(root)
        binaries={}
        for name in ['resource_bench.exe','bench_adapter.exe']:
            p=root/'target/measure'/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(b'synthetic executable');binaries[name]=sha(p)
        samples=[{'elapsedSeconds':i*5,'jobCpuSeconds':i*.05,'freeDiskBytes':10**10,'processes':[{'privateWorkingSet':1000,'handles':2}],'workload':{'elapsedSeconds':i*5,'phase':'idle' if i*5<1030 else 'four-recordings' if i*5<2830 else 'mixed','queryTimesMs':[1,2,3,4] if i==0 or i*5>=1030 else [],'explorerTimesMs':[5,6,7,8,9] if i==0 or i*5>=1030 else [],'throughputBytes':i*1000,'jobs':{'activeRecordings':0 if i*5<1030 else 4,'activeDownloads':2 if i*5>=2830 else 0,'queueDepth':1}}} for i in range(928)]
        raw={'samples':samples,'workload':{'idleSeconds':1030,'metadataEntries':100000,'mediaMetadata':500000},'logicalProcessors':8,'sourceInputs':source,'lifecycle':{'jobErrors':0,'recordingStarts':[{'observableUnix':90,'pollIntervalSeconds':30,'detectedUnix':100,'firstReceiveUnix':101}]*4,'recovery':{'startedElapsed':5000,'finishedElapsed':5001},'gui':[{'mode':'open','readyElapsed':3000},{'mode':'closed','requestedElapsed':3010,'releasedElapsed':3011},{'mode':'reconnected','readyElapsed':3020}]},'build':{'exitCode':0,'log':'raw.log','logSha256':sha(root/'raw.log'),'binaries':binaries}}
        raw['guiExecutableSha256']=sha(root/'package/trace-desktop.exe')
        raw['workload']['queryCacheState']='new_core_connection_os_cache_unflushed'
        raw['workload'].update(bodyMedianChars=512,ordinaryCheckTargets=100,liveCheckTargets=20,httpLatencyMs=100,queryPageSize=50,explorerEntries=100000,explorerQueriesPerRound=5,streamBytesPerSecond=1000000)
        raw['lifecycle']['recovery'].update(unfinishedOrdinaryTasks=100,serviceAccessible=True)
        raw['lifecycle']['guiProbe']=[{'instanceId':'synthetic','embeddedPage':True,'lifecycleControls':True,'schemaVersion':6,'acquisitionQueries':True,'sourceQueries':True} for _ in range(2)]
        (root/'samples.json').write_text(json.dumps(raw))
        bundle={'plan':'TRACE-M2-01 r1','schemaVersion':6,'requirementCoverage':requirement_coverage(root,parse_cases((root/'raw.log').read_text())),'sourceInputs':source,'productAcceptance':'not-run','cases':parse_cases((root/'raw.log').read_text()),
          'commands':[{'id':'test','exitCode':0,'sourceInputs':source,'log':'raw.log','logSha256':sha(root/'raw.log')}],
          'package':{'directory':'package','sourceInputs':source,'files':inventory(root/'package')},
          'benchmark':{'exitCode':0,'status':'passed','sourceInputs':source,'sourceUnchanged':True,**derive(raw),'samples':'samples.json','samplesSha256':sha(root/'samples.json')}}
        receipt=bundle['commands'][0]
        other=dict(receipt,log='other.log',logSha256=sha(root/'other.log'))
        bundle['commands']=[dict(receipt if kind=='test' else other,id=kind,kind=kind,sourceUnchanged=True,cases=copy.deepcopy(bundle['cases']) if kind=='test' else []) for kind in ['test','bench','package','report']]
        bundle['ruleEvidence']={'path':'docs/evidence/rule-evidence.json','sha256':sha(root/'docs/evidence/rule-evidence.json')}
        return bundle,source

    def package_sums(self,root):
        (root/'package/SHA256SUMS.txt').write_text(''.join(f'{h}  {n}\n' for n,h in inventory(root/'package').items() if n!='SHA256SUMS.txt'))

    def test_product_inputs_exclude_documents_and_scope_comes_from_the_map(self):
        names=inputs()
        for name in ['.env.example','crates/core/src/db/schema.rs','Cargo.lock','media-tools.lock.json']:
            self.assertIn(name,names)
        self.assertEqual([n for n in names if n.startswith('docs/') or n.endswith('.md')],[],'documents never make package or test evidence stale')
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root)
            check=lambda b:validate(b,root,source,{'C01':['required']})
            self.assertEqual(check(base),[])
            for mutate in [lambda b:b.update(plan='TRACE-M2-01 r0'),lambda b:b.update(schemaVersion=4),lambda b:b.update(requirementCoverage=[]),lambda b:b['requirementCoverage'][0].update(status='not-run'),lambda b:b['requirementCoverage'][-1].update(status='passed')]:
                changed=copy.deepcopy(base);mutate(changed);self.assertTrue(check(changed))
            mapping=json.loads((root/'docs/delivery/requirements-map.json').read_text())
            for mutate in [lambda m:m['requirements'][0]['current'].update(testCases=['not-executed-case']),lambda m:m['requirements'][0].pop('current')]:
                changed=copy.deepcopy(mapping);mutate(changed)
                (root/'docs/delivery/requirements-map.json').write_text(json.dumps(changed))
                bad=copy.deepcopy(base);bad['requirementCoverage']=requirement_coverage(root,base['cases'])
                self.assertTrue(check(bad),'a case that did not run must not satisfy the current scope')

    def test_a_document_edit_does_not_change_the_product_fingerprint(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder)
            git=lambda *args:subprocess.run(['git',*args],cwd=root,check=True,capture_output=True)
            git('init','-q')
            for name,text in [('crates/core/src/lib.rs','fn a() {}'),('docs/design.md','one'),('docs/evidence/rule-evidence.json','{}'),('README.md','readme'),('media-tools.lock.json','{}')]:
                path=root/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_text(text)
            before=inputs(root)
            self.assertIn('crates/core/src/lib.rs',before)
            self.assertIn('media-tools.lock.json',before)
            for name in ['docs/design.md','docs/evidence/rule-evidence.json','README.md']:
                self.assertNotIn(name,before)
                (root/name).write_text('edited')
            (root/'docs/new-note.md').write_text('new')
            self.assertEqual(inputs(root),before,'documents and the public rule record never make evidence stale')
            (root/'crates/core/src/lib.rs').write_text('fn b() {}')
            self.assertNotEqual(inputs(root),before)
            (root/'media-tools.lock.json').write_text('{"yt_dlp":{}}')
            self.assertNotEqual(inputs(root)['media-tools.lock.json'],before['media-tools.lock.json'])

    def test_the_package_must_carry_every_adapter_and_the_pinned_tools(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root);package=root/'package'
            self.assertEqual(validate_package(package,source,root),[])
            self.assertEqual(sorted(package_layout(root)['executables']),sorted(n for n in inventory(package) if n.endswith('.exe')))
            def broken(change,text):
                saved={}
                change(saved)
                self.package_sums(root)
                errors=validate_package(package,source,root)
                self.assertTrue(any(text in e for e in errors) or text=='' and errors,f'{text}: {errors}')
                for path,data in saved.items():
                    if data is None:path.unlink()
                    else:path.write_bytes(data)
                self.package_sums(root)
            def drop(name):
                def change(saved):
                    path=package/name;saved[path]=path.read_bytes();path.unlink()
                return change
            def edit(name,data):
                def change(saved):
                    path=package/name;saved[path]=path.read_bytes();path.write_bytes(data)
                return change
            broken(drop('adapters/trace-adapter-beta.exe'),'')
            broken(drop('tools/deno.exe'),'')
            broken(drop('media/SOURCE.txt'),'missing package legal material')
            broken(drop('tools/yt-dlp-LICENSE.txt'),'')
            broken(drop('tools/verified.json'),'invalid package manifest or build evidence')
            broken(edit('tools/yt-dlp.exe',b'replaced'),'package tool file changed')
            def extra(saved):
                path=package/'adapters/trace-adapter-gamma.exe';path.write_bytes(b'unlisted');saved[path]=None
            broken(extra,'extra or missing package executable')
            def other_pin(saved):
                path=package/'tools/verified.json';saved[path]=path.read_bytes()
                data=json.loads(saved[path]);data['files']['deno.exe']='0'*64;path.write_text(json.dumps(data))
            broken(other_pin,'')
            self.assertEqual(validate_package(package,source,root),[])

    def test_a_new_site_without_a_real_receipt_cannot_be_implemented(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root)
            path=root/'docs/evidence/rule-evidence.json'
            original=json.loads(path.read_text(encoding='utf8'))
            def errors_after(change,keep_hash=False):
                data=copy.deepcopy(original);change(data)
                path.write_text(json.dumps(data),encoding='utf8')
                reference={'path':'docs/evidence/rule-evidence.json','sha256':sha(path)}
                return validate_rule_evidence(reference,root)
            self.assertEqual(errors_after(lambda d:None),[])
            bad_cases=[
                (lambda d:d['sites']['alpha']['rules']['alpha.user.posts'].update(checks=[]),'without passed checks'),
                (lambda d:d['sites']['alpha']['rules']['alpha.user.posts']['checks'][1].update(outcome='failed'),'without passed checks'),
                (lambda d:d['sites']['alpha']['rules']['alpha.user.posts']['checks'][3].update(outcome='not-run'),'without passed checks'),
                (lambda d:d['sites']['alpha']['rules']['alpha.user.posts'].update(checks=d['sites']['alpha']['rules']['alpha.user.posts']['checks'][:2]),'without passed checks'),
                (lambda d:d['sites']['alpha']['rules']['alpha.user.posts']['checks'].append({'name':'extra','outcome':'stopped'}),'failed or stopped'),
                (lambda d:d['sites']['alpha']['rules']['alpha.user.posts'].pop('scope'),'verification date and scope'),
                (lambda d:d['sites']['alpha']['rules']['alpha.user.posts'].pop('verifiedAt'),'verification date and scope'),
                (lambda d:d['sites']['alpha']['rules']['alpha.post.link'].pop('note'),'must say what is missing'),
                (lambda d:d['sites']['beta']['rules']['beta.user.preview'].update(note=' '),'must say what is missing'),
                (lambda d:d['sites']['alpha']['rules']['alpha.post.link'].update(status='implemented',verifiedAt='2026-10-18',scope='photos'),'without passed checks'),
                (lambda d:d['sites'].pop('beta'),'sites differ'),
                (lambda d:d['sites'].update(gamma=copy.deepcopy(d['sites']['beta'])),'sites differ'),
                (lambda d:d['sites']['alpha']['rules'].update({'beta.user.posts':d['sites']['alpha']['rules']['alpha.post.link']}),'does not belong'),
                (lambda d:d['sites']['alpha']['run'].update(metadataRequests=61),'exceed the authorized budget'),
                (lambda d:d['sites']['alpha']['run'].update(mediaBytes=10**6+1),'media bytes exceed'),
                (lambda d:d['sites']['alpha']['run'].update(metadataRequests=-1),'non-negative'),
                (lambda d:d['sites']['alpha']['run'].update(stoppedBy='made-up'),'stop reason'),
                (lambda d:d['sites']['alpha']['rules']['alpha.post.link']['checks'].append({'name':'x','outcome':'maybe'}),'malformed check'),
                (lambda d:d.update(plan='older plan'),'wrong plan'),
                (lambda d:d.update(schemaVersion=2),'schema version'),
            ]
            for change,text in bad_cases:
                self.assertTrue(any(text in e for e in errors_after(change)),text)
            # The record is bound by hash: editing it after the report makes the report stale.
            path.write_text(json.dumps(original),encoding='utf8')
            stale={'path':'docs/evidence/rule-evidence.json','sha256':'0'*64}
            self.assertTrue(any('changed after the report' in e for e in validate_rule_evidence(stale,root)))
            self.assertEqual(validate_rule_evidence(None,root),['missing rule evidence reference'])
            path.unlink()
            self.assertEqual(validate_rule_evidence({'path':'docs/evidence/rule-evidence.json','sha256':'0'*64},root),['missing rule evidence file'])
            # A tampered authorization (the ceiling a record may not raise by itself) is rejected.
            path.write_text(json.dumps(original),encoding='utf8')
            text=(root/'USER-ACTIONS.md').read_text(encoding='utf8')
            for broken in ['',text+text,text.replace('"metadata_requests": 60','"metadata_requests": true')]:
                (root/'USER-ACTIONS.md').write_text(broken,encoding='utf8')
                reference={'path':'docs/evidence/rule-evidence.json','sha256':sha(path)}
                self.assertTrue(validate_rule_evidence(reference,root),'missing, duplicate or malformed authorization is rejected')

    def test_rejects_package_manifest_and_build_tampering(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root)
            manifest=json.loads((root/'package/manifest.json').read_text())
            for mutate in [lambda m:m.update(built_binaries={}),lambda m:m.update(sourceInputs={}),lambda m:m.update(source_tree_sha256='wrong'),lambda m:m.update(components=[]),lambda m:m['build_commands'][0].update(exitCode=1),lambda m:m['build_commands'][0].update(logSha256='wrong'),lambda m:m['build_commands'][0].update(sourceUnchanged=False),lambda m:[c.update(argv=[c['argv'][0],'build']) for c in m['build_commands']],lambda m:m.update(productAcceptance='passed')]:
                m=copy.deepcopy(manifest);mutate(m);(root/'package/manifest.json').write_text(json.dumps(m))
                self.package_sums(root);bad=copy.deepcopy(base);bad['package']['files']=inventory(root/'package')
                self.assertTrue(validate(bad,root,source,{'C01':['required']}))
            for malformed in [None,[],{'commands':None},{'cases':[None]}]:
                self.assertTrue(validate(malformed,root,source,{'C01':['required']}))

    def test_rejects_traversal_absolute_and_linked_evidence(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root)
            for name in ['../secret','/outside','C:/private','package/../../private','package\\private','']:
                with self.assertRaises(ValueError):contained(root,name)
            target=root/'target-directory';target.mkdir();link=root/'linked-directory'
            try:
                os.symlink(target,link,target_is_directory=True)
            except OSError:
                if os.name!='nt':raise
                subprocess.run(['powershell','-NoProfile','-Command','New-Item -ItemType Junction -Path $env:TRACE_TEST_LINK -Target $env:TRACE_TEST_TARGET | Out-Null'],env=dict(os.environ,TRACE_TEST_LINK=str(link),TRACE_TEST_TARGET=str(target)),check=True,capture_output=True,creationflags=subprocess.CREATE_NO_WINDOW)
            try:
                with self.assertRaises(ValueError):contained(root,'linked-directory/evidence.json')
            finally:
                if link.is_symlink():link.unlink()
                else:os.rmdir(link)
            bad=copy.deepcopy(base);bad['package']['directory']='../outside'
            self.assertIn('invalid package path',validate(bad,root,source,{'C01':['required']}))
            bad=copy.deepcopy(base);bad['commands'][0]['cases'][0]['name']='invented'
            self.assertTrue(any('raw log' in e for e in validate(bad,root,source,{'C01':['required']})))

    def test_rejects_stale_source_missing_failed_duplicate_cases(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root)
            check=lambda b:validate(b,root,source,{'C01':['required']})
            self.assertEqual(check(base),[])
            for mutate in [lambda b:b.update(sourceInputs={'code':'old'}),lambda b:b.update(cases=[]),lambda b:b['cases'][0].update(status='failed'),lambda b:b['cases'].append(copy.deepcopy(b['cases'][0]))]:
                bad=copy.deepcopy(base);mutate(bad);self.assertTrue(check(bad))
            for mutate in [lambda b:b['commands'][0].update(sourceUnchanged=False),lambda b:b['commands'][0].update(cases=[]),lambda b:b['commands'].pop(),lambda b:b['commands'].append(copy.deepcopy(b['commands'][0]))]:
                bad=copy.deepcopy(base);mutate(bad);self.assertTrue(check(bad))

    def test_rejects_missing_package_failed_and_short_benchmark(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root)
            check=lambda b:validate(b,root,source,{'C01':['required']})
            for mutate in [lambda b:b['package']['files'].pop('trace-daemon.exe'),lambda b:b['benchmark'].update(exitCode=1),lambda b:b['benchmark'].update(durationSeconds=3599),lambda b:b.update(productAcceptance='passed'),lambda b:b['benchmark']['measurements'].update(queryP95Ms=0),lambda b:b['benchmark'].update(metadataEntries=200000)]:
                bad=copy.deepcopy(base);mutate(bad);self.assertTrue(check(bad))
            raw=json.loads((root/'samples.json').read_text())
            for mutate in [lambda r:r.update(guiExecutableSha256='wrong'),lambda r:r['lifecycle'].update(guiProbe=[]),lambda r:r['lifecycle']['guiProbe'][1].update(instanceId='other-daemon'),lambda r:r['lifecycle']['guiProbe'][0].pop('acquisitionQueries'),lambda r:r['workload'].pop('queryCacheState'),lambda r:r['lifecycle']['recovery'].update(unfinishedOrdinaryTasks=99),lambda r:r['lifecycle']['recovery'].update(serviceAccessible=False)]:
                changed=copy.deepcopy(raw);mutate(changed);(root/'samples.json').write_text(json.dumps(changed));bad=copy.deepcopy(base);bad['benchmark']['samplesSha256']=sha(root/'samples.json');self.assertTrue(check(bad))
            changed=copy.deepcopy(raw)
            for sample in changed['samples']:
                if sample['workload']['jobs']['activeDownloads']>=2:sample['workload']['jobs']['activeRecordings']=0
            (root/'samples.json').write_text(json.dumps(changed));bad=copy.deepcopy(base)
            bad['benchmark'].update(derive(changed),samplesSha256=sha(root/'samples.json'))
            self.assertGreaterEqual(bad['benchmark']['parallelRecordings'],4)
            self.assertGreaterEqual(bad['benchmark']['parallelDownloads'],2)
            self.assertTrue(check(bad),'separate maxima cannot prove simultaneous downloads and recording')
            for key,value in [('bodyMedianChars',400),('ordinaryCheckTargets',99),('liveCheckTargets',19),('httpLatencyMs',99),('queryPageSize',49),('explorerEntries',99999),('streamBytesPerSecond',999999)]:
                changed=copy.deepcopy(raw);changed['workload'][key]=value
                (root/'samples.json').write_text(json.dumps(changed));bad=copy.deepcopy(base)
                bad['benchmark'].update(derive(changed),samplesSha256=sha(root/'samples.json'))
                self.assertTrue(any(key in error for error in check(bad)),key+' must be measured at the required workload')
            (root/'package/trace-daemon.exe').unlink();self.assertTrue(check(base))

    def test_the_duration_is_the_workloads_own_end_not_the_last_five_second_sample(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root)
            raw=json.loads((root/'samples.json').read_text())
            idle=raw['workload']['idleSeconds']
            # The last sample falls 3 seconds before the run ends.
            raw['samples']=[s for s in raw['samples'] if s['workload']['elapsedSeconds']<=idle+3595]
            short=derive(raw)['durationSeconds']
            self.assertLess(short,3600,'the samples alone stop at 3595 seconds')
            raw['finalWorkload']={'elapsedSeconds':idle+3601,'phase':'mixed'}
            self.assertEqual(derive(raw)['durationSeconds'],3601)
            # The final progress cannot claim more than one sampling interval past the samples.
            raw['finalWorkload']={'elapsedSeconds':idle+3595+16,'phase':'mixed'}
            with self.assertRaises(ValueError):derive(raw)
            raw['finalWorkload']={'elapsedSeconds':float('nan')}
            with self.assertRaises(ValueError):derive(raw)
            # Without a final progress the run is as long as it was sampled.
            raw.pop('finalWorkload');self.assertEqual(derive(raw)['durationSeconds'],short)

    def test_a_slow_explorer_fails_the_release_measurement(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source=self.fixture(root)
            check=lambda b:validate(b,root,source,{'C01':['required']})
            self.assertEqual(check(base),[])
            raw=json.loads((root/'samples.json').read_text())
            changed=copy.deepcopy(raw)
            for sample in changed['samples'][1:]:
                if sample['workload']['explorerTimesMs']:sample['workload']['explorerTimesMs']=[900]*5
            (root/'samples.json').write_text(json.dumps(changed));bad=copy.deepcopy(base)
            bad['benchmark'].update(derive(changed),samplesSha256=sha(root/'samples.json'))
            self.assertTrue(any('explorerHotP95Ms' in error for error in check(bad)),'a slow explorer must fail the release measurement')
            changed=copy.deepcopy(raw)
            for sample in changed['samples']:sample['workload'].pop('explorerTimesMs',None)
            (root/'samples.json').write_text(json.dumps(changed));bad=copy.deepcopy(base)
            bad['benchmark'].update(derive(changed),samplesSha256=sha(root/'samples.json'))
            self.assertTrue(any('explorer' in error for error in check(bad)),'a run that never queried the explorer proves nothing')


if __name__=='__main__':unittest.main()
