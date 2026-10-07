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
from stage_evidence import validate,sha,parse_cases,contained,inventory,requirement_coverage,inputs,ROOT
from benchmark_metrics import derive
from real_chain_fixtures import real_fixture

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
        files=['trace-desktop.exe','trace-daemon.exe','adapters/trace-adapter-bilibili.exe','adapters/trace-adapter-douyin.exe','adapters/douyin-NOTICE.txt','media/ffmpeg.exe','media/ffprobe.exe','media/ffplay.exe','dependency-licenses.md','media/LICENSE.txt','media/SOURCE.txt','LICENSE']
        for name in files:
            p=root/'package'/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(b'isolated gate fixture')
        (root/'raw.log').write_text('test required ... ok\n')
        source={'code':'a','tests':'b','lock':'c'};docs={'report':'d'}
        (root/'docs/delivery').mkdir(parents=True);(root/'crates/domain/src').mkdir(parents=True)
        (root/'docs/delivery/stage-plan.md').write_text('TRACE-M1-01 r3')
        (root/'crates/domain/src/lib.rs').write_text('pub const SCHEMA_VERSION: i64 = 6;')
        mapping={'requirements':[{'id':f'FR-fixture-{i:03}','m1':{'applicable':i<68,'testCases':['required'] if i<68 else [],'implementation':['code'] if i<68 else []}} for i in range(87)]}
        for row in mapping['requirements']:
            row['r2']=copy.deepcopy(row['m1']);row['r3']=dict(copy.deepcopy(row['r2']),plan='TRACE-M1-01 r3',baselineMapping='r2')
        (root/'docs/delivery/requirements-map.json').write_text(json.dumps(mapping))
        (root/'media-backend.lock.json').write_text('{}')
        components=files[:4]
        manifest={'sourceInputs':source,'source_files_sha256':source,'source_tree_sha256':hashlib.sha256(json.dumps(source,sort_keys=True,separators=(',',':')).encode()).hexdigest(),
            'stage':'TRACE-M1-01 r3','productAcceptance':'not-run','schema_version':6,'components':components,'built_binaries':{n:sha(root/'package'/n) for n in components},
            'build_commands':[{'argv':[cmd,'trace-desktop/custom-protocol'],'exitCode':0,'sourceInputs':source,'sourceUnchanged':True,'log':'raw.log','logSha256':sha(root/'raw.log')} for cmd in ['cargo','corepack.cmd']]}
        (root/'package/manifest.json').write_text(json.dumps(manifest))
        (root/'package/profile.example.json').write_text(json.dumps({'test_allowed_hosts':[],'extra_media_hosts':[]}))
        (root/'package/media/verified.json').write_text(json.dumps({'lock':{},'files':{n:sha(root/'package/media'/n) for n in ['ffmpeg.exe','ffprobe.exe','ffplay.exe']}}))
        self.package_sums(root)
        binaries={}
        for name in ['m1_resource_bench.exe','m1_bench_adapter.exe']:
            p=root/'target/measure'/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(b'synthetic executable');binaries[name]=sha(p)
        samples=[{'elapsedSeconds':i*5,'jobCpuSeconds':i*.05,'freeDiskBytes':10**10,'processes':[{'privateWorkingSet':1000,'handles':2}],'workload':{'elapsedSeconds':i*5,'phase':'idle' if i*5<1030 else 'four-recordings' if i*5<2830 else 'mixed','queryTimesMs':[1,2,3,4] if i==0 or i*5>=1030 else [],'throughputBytes':i*1000,'jobs':{'activeRecordings':0 if i*5<1030 else 4,'activeDownloads':2 if i*5>=2830 else 0,'queueDepth':1}}} for i in range(928)]
        raw={'samples':samples,'workload':{'idleSeconds':1030,'metadataEntries':100000,'mediaMetadata':500000},'logicalProcessors':8,'sourceInputs':source,'lifecycle':{'jobErrors':0,'recordingStarts':[{'observableUnix':90,'pollIntervalSeconds':30,'detectedUnix':100,'firstReceiveUnix':101}]*4,'recovery':{'startedElapsed':5000,'finishedElapsed':5001},'gui':[{'mode':'open','readyElapsed':3000},{'mode':'closed','requestedElapsed':3010,'releasedElapsed':3011},{'mode':'reconnected','readyElapsed':3020}]},'build':{'exitCode':0,'log':'raw.log','logSha256':sha(root/'raw.log'),'binaries':binaries}}
        raw['guiExecutableSha256']=sha(root/'package/trace-desktop.exe')
        raw['workload']['queryCacheState']='new_core_connection_os_cache_unflushed'
        raw['workload'].update(bodyMedianChars=512,ordinaryCheckTargets=100,liveCheckTargets=20,httpLatencyMs=100,queryPageSize=50,streamBytesPerSecond=1000000)
        raw['lifecycle']['recovery'].update(unfinishedOrdinaryTasks=100,serviceAccessible=True)
        raw['lifecycle']['guiProbe']=[{'instanceId':'synthetic','embeddedPage':True,'lifecycleControls':True,'schemaVersion':6,'r2AcquisitionQueries':True,'r3SourceQueries':True} for _ in range(2)]
        (root/'samples.json').write_text(json.dumps(raw))
        bundle={'plan':'TRACE-M1-01 r3','schemaVersion':6,'requirementCoverage':requirement_coverage(root,parse_cases((root/'raw.log').read_text())),'sourceInputs':source,'documents':docs,'productAcceptance':'not-run','cases':parse_cases((root/'raw.log').read_text()),
          'commands':[{'id':'test','exitCode':0,'sourceInputs':source,'log':'raw.log','logSha256':sha(root/'raw.log')}],
          'package':{'directory':'package','sourceInputs':source,'files':inventory(root/'package')},
          'benchmark':{'exitCode':0,'status':'passed','sourceInputs':source,'sourceUnchanged':True,**derive(raw),'samples':'samples.json','samplesSha256':sha(root/'samples.json')}}
        receipt=bundle['commands'][0]
        bundle['commands']=[dict(receipt,id=kind,kind=kind,sourceUnchanged=True,cases=copy.deepcopy(bundle['cases']) if kind=='test' else []) for kind in ['test','bench','package','report']]
        bundle["realSource"]=real_fixture(root,source,bundle["package"])
        return bundle,source,docs

    def test_r2_contract_requirements_and_configuration_are_required_inputs(self):
        names=inputs()
        for name in ['.env.example','docs/delivery/stage-plan.md','docs/site-archive-contracts.md','docs/requirements.md','crates/core/src/db/schema.rs']:
            self.assertIn(name,names)
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source,docs=self.fixture(root)
            check=lambda b:validate(b,root,source,docs,{'C01':['required']})
            self.assertEqual(check(base),[])
            for mutate in [lambda b:b.update(plan='TRACE-M1-01 r1'),lambda b:b.update(schemaVersion=4),lambda b:b.update(requirementCoverage=[]),lambda b:b['requirementCoverage'][0].update(status='not-run'),lambda b:b['requirementCoverage'][-1].update(status='passed')]:
                changed=copy.deepcopy(base);mutate(changed);self.assertTrue(check(changed))
            mapping=json.loads((root/'docs/delivery/requirements-map.json').read_text())
            for mutate in [lambda m:m['requirements'][0]['r2'].update(testCases=['not-executed-r2-case']),lambda m:m['requirements'][0].pop('r2')]:
                changed=copy.deepcopy(mapping);mutate(changed)
                (root/'docs/delivery/requirements-map.json').write_text(json.dumps(changed))
                bad=copy.deepcopy(base);bad['requirementCoverage']=requirement_coverage(root,base['cases'])
                self.assertTrue(check(bad),'r1 coverage must not stand in for missing r2 evidence')

    def test_r3_rejects_missing_diagnostic_stale_low_resolution_and_incomplete_real_chain(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source,docs=self.fixture(root);check=lambda b:validate(b,root,source,docs,{'C01':['required']})
            self.assertEqual(check(base),[])
            missing=copy.deepcopy(base);missing.pop('realSource');self.assertTrue(check(missing))
            receipt=json.loads((root/'real-fixture.json').read_text())
            for mutate in [lambda r:r.update(origin='source_diagnostic'),lambda r:r.update(sourceInputs={'old':'source'}),lambda r:r.update(packageManifestSha256='old'),lambda r:r['cases'].pop(),lambda r:r['cases'][0].update(guiSelection=False),lambda r:r['cases'][0].update(objects=[]),lambda r:r['cases'][1].update(receivedSeconds=59),lambda r:r['cases'][1].update(sealed=False),lambda r:r['cases'][1].update(exported=False),lambda r:r['cases'][1].update(guiClosedDuringCapture=False),lambda r:r['cases'][0]['decodes'][0].update(exitCode=1),lambda r:r['networkBudget'].update(stopped=True),lambda r:r['cases'][0].update(offlineReads=[]),lambda r:r['cases'][0].update(exports=[]),lambda r:r['cases'][0].update(sourceDurationSeconds=1),lambda r:r['cases'][1]['lifecycle'].update(continuedCoreInstanceId='changed'),lambda r:r['cases'][0]['decodes'][0].update(inputSha256='unrelated')]:
                changed=copy.deepcopy(receipt);mutate(changed);(root/'real-fixture.json').write_text(json.dumps(changed));bad=copy.deepcopy(base);bad['realSource'][0]['sha256']=sha(root/'real-fixture.json');self.assertTrue(check(bad))
            for media in [('bilibili-live',640,360),('bilibili-video',1920,1080)]:
                (root/'real-fixture.json').write_text(json.dumps(receipt));probe=root/(media[0]+'-probe.json');saved=probe.read_text();value=json.loads(saved);value['streams'][0].update(width=media[1],height=media[2]);probe.write_text(json.dumps(value));changed=copy.deepcopy(receipt)
                for item in changed['cases']:
                    if item['requestId']==media[0]:item['probe']['sha256']=sha(probe)
                (root/'real-fixture.json').write_text(json.dumps(changed));bad=copy.deepcopy(base);bad['realSource'][0]['sha256']=sha(root/'real-fixture.json');self.assertTrue(check(bad));probe.write_text(saved)
            changed=copy.deepcopy(receipt);probe=root/changed['cases'][0]['probe']['path'];value=json.loads(probe.read_text());value['streams']=value['streams'][:1];probe.write_text(json.dumps(value));changed['cases'][0]['probe']['sha256']=sha(probe);(root/'real-fixture.json').write_text(json.dumps(changed));bad=copy.deepcopy(base);bad['realSource'][0]['sha256']=sha(root/'real-fixture.json');self.assertTrue(check(bad),'missing necessary audio cannot pass')
            mapping=json.loads((root/'docs/delivery/requirements-map.json').read_text());mapping['requirements'][0].pop('r3');(root/'docs/delivery/requirements-map.json').write_text(json.dumps(mapping));self.assertTrue(check(base),'r2 objects cannot replace independent r3 coverage')

    def package_sums(self,root):
        (root/'package/SHA256SUMS.txt').write_text(''.join(f'{h}  {n}\n' for n,h in inventory(root/'package').items() if n!='SHA256SUMS.txt'))

    def test_r3_real_catalog_rejects_wrong_owner_unsealed_and_false_duration(self):
        import sqlite3
        from contextlib import closing
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source,docs=self.fixture(root)
            check=lambda b:validate(b,root,source,docs,{'C01':['required']})
            self.assertEqual(check(base),[])
            receipt=json.loads((root/'real-fixture.json').read_text());path=root/receipt['database']['path'];original=path.read_bytes()
            for sql in ["UPDATE live_fragments SET end_ms=59000", "UPDATE live_sessions SET state='recording'", "UPDATE acquisition_contexts SET credential_id='other'", "UPDATE materializations SET job_id='another-job'", "UPDATE media_variants SET approval_state='rejected'", "UPDATE source_requests SET snapshot='{\"source_duration_seconds\":999}'", "DELETE FROM asset_selections"]:
                path.write_bytes(original)
                with closing(sqlite3.connect(path)) as db,db:db.execute(sql)
                changed=copy.deepcopy(receipt);changed['database']['sha256']=sha(path);(root/'real-fixture.json').write_text(json.dumps(changed))
                bad=copy.deepcopy(base);bad['realSource'][0]['sha256']=sha(root/'real-fixture.json')
                self.assertTrue(check(bad),sql)

    def test_r3_real_prior_run_budget_is_cumulative(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source,docs=self.fixture(root);check=lambda b:validate(b,root,source,docs,{'C01':['required']})
            receipt=json.loads((root/'real-fixture.json').read_text());prior=root/'prior-budget.json'
            for count,media,valid in [(1,0,True),(29,0,False),(0,512*1024*1024,False)]:
                prior.write_text(json.dumps({'attempts':[{'metadata':True}]*count,'media_bytes':media,'media_reserved':media}))
                changed=copy.deepcopy(receipt);changed['priorRuns']=[{'path':prior.name,'sha256':sha(prior)}];(root/'real-fixture.json').write_text(json.dumps(changed))
                updated=copy.deepcopy(base);updated['realSource'][0]['sha256']=sha(root/'real-fixture.json')
                self.assertEqual(not bool(check(updated)),valid)
            changed=copy.deepcopy(receipt);changed['authorizedBudget']['metadata_requests']=40
            (root/'real-fixture.json').write_text(json.dumps(changed));updated=copy.deepcopy(base);updated['realSource'][0]['sha256']=sha(root/'real-fixture.json')
            self.assertTrue(check(updated),'a receipt cannot authorize its own larger budget')
            policy=(root/'USER-ACTIONS.md').read_text(encoding='utf8')
            for text in ['',policy+policy,policy.replace('30','true')]:
                (root/'USER-ACTIONS.md').write_text(text,encoding='utf8')
                self.assertTrue(check(updated),'missing, duplicate or malformed authorization is rejected')

    def test_rejects_package_manifest_and_build_tampering(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source,docs=self.fixture(root)
            manifest=json.loads((root/'package/manifest.json').read_text())
            for mutate in [lambda m:m.update(built_binaries={}),lambda m:m.update(sourceInputs={}),lambda m:m.update(source_tree_sha256='wrong'),lambda m:m.update(components=[]),lambda m:m['build_commands'][0].update(exitCode=1),lambda m:m['build_commands'][0].update(logSha256='wrong'),lambda m:m['build_commands'][0].update(sourceUnchanged=False),lambda m:[c.update(argv=[c['argv'][0],'build']) for c in m['build_commands']],lambda m:m.update(productAcceptance='passed')]:
                m=copy.deepcopy(manifest);mutate(m);(root/'package/manifest.json').write_text(json.dumps(m))
                self.package_sums(root);bad=copy.deepcopy(base);bad['package']['files']=inventory(root/'package')
                self.assertTrue(validate(bad,root,source,docs,{'C01':['required']}))
            for malformed in [None,[],{'commands':None},{'cases':[None]}]:
                self.assertTrue(validate(malformed,root,source,docs,{'C01':['required']}))

    def test_rejects_traversal_absolute_and_linked_evidence(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source,docs=self.fixture(root)
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
            self.assertIn('invalid package path',validate(bad,root,source,docs,{'C01':['required']}))
            bad=copy.deepcopy(base);bad['commands'][0]['cases'][0]['name']='invented'
            self.assertTrue(any('raw log' in e for e in validate(bad,root,source,docs,{'C01':['required']})))

    def test_rejects_stale_source_missing_failed_duplicate_cases(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source,docs=self.fixture(root)
            check=lambda b:validate(b,root,source,docs,{'C01':['required']})
            self.assertEqual(check(base),[])
            for mutate in [lambda b:b.update(sourceInputs={'code':'old'}),lambda b:b.update(cases=[]),lambda b:b['cases'][0].update(status='failed'),lambda b:b['cases'].append(copy.deepcopy(b['cases'][0]))]:
                bad=copy.deepcopy(base);mutate(bad);self.assertTrue(check(bad))
            for mutate in [lambda b:b['commands'][0].update(sourceUnchanged=False),lambda b:b['commands'][0].update(cases=[]),lambda b:b['commands'].pop(),lambda b:b['commands'].append(copy.deepcopy(b['commands'][0]))]:
                bad=copy.deepcopy(base);mutate(bad);self.assertTrue(check(bad))

    def test_rejects_missing_package_failed_short_benchmark_and_stale_docs(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);base,source,docs=self.fixture(root)
            check=lambda b:validate(b,root,source,docs,{'C01':['required']})
            for mutate in [lambda b:b['package']['files'].pop('trace-daemon.exe'),lambda b:b['benchmark'].update(exitCode=1),lambda b:b['benchmark'].update(durationSeconds=3599),lambda b:b.update(documents={'report':'stale'}),lambda b:b.update(productAcceptance='passed'),lambda b:b['benchmark']['measurements'].update(queryP95Ms=0),lambda b:b['benchmark'].update(metadataEntries=200000)]:
                bad=copy.deepcopy(base);mutate(bad);self.assertTrue(check(bad))
            raw=json.loads((root/'samples.json').read_text())
            for mutate in [lambda r:r.update(guiExecutableSha256='wrong'),lambda r:r['lifecycle'].update(guiProbe=[]),lambda r:r['lifecycle']['guiProbe'][1].update(instanceId='other-daemon'),lambda r:r['lifecycle']['guiProbe'][0].pop('r2AcquisitionQueries'),lambda r:r['workload'].pop('queryCacheState'),lambda r:r['lifecycle']['recovery'].update(unfinishedOrdinaryTasks=99),lambda r:r['lifecycle']['recovery'].update(serviceAccessible=False)]:
                changed=copy.deepcopy(raw);mutate(changed);(root/'samples.json').write_text(json.dumps(changed));bad=copy.deepcopy(base);bad['benchmark']['samplesSha256']=sha(root/'samples.json');self.assertTrue(check(bad))
            changed=copy.deepcopy(raw)
            for sample in changed['samples']:
                if sample['workload']['jobs']['activeDownloads']>=2:sample['workload']['jobs']['activeRecordings']=0
            (root/'samples.json').write_text(json.dumps(changed));bad=copy.deepcopy(base)
            bad['benchmark'].update(derive(changed),samplesSha256=sha(root/'samples.json'))
            self.assertGreaterEqual(bad['benchmark']['parallelRecordings'],4)
            self.assertGreaterEqual(bad['benchmark']['parallelDownloads'],2)
            self.assertTrue(check(bad),'separate maxima cannot prove simultaneous downloads and recording')
            for key,value in [('bodyMedianChars',400),('ordinaryCheckTargets',99),('liveCheckTargets',19),('httpLatencyMs',99),('queryPageSize',49),('streamBytesPerSecond',999999)]:
                changed=copy.deepcopy(raw);changed['workload'][key]=value
                (root/'samples.json').write_text(json.dumps(changed));bad=copy.deepcopy(base)
                bad['benchmark'].update(derive(changed),samplesSha256=sha(root/'samples.json'))
                self.assertTrue(any(key in error for error in check(bad)),key+' must be measured at the required workload')
            (root/'package/trace-daemon.exe').unlink();self.assertTrue(check(base))

if __name__=='__main__':unittest.main()
