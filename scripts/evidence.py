#!/usr/bin/env python3
"""Evidence library: command receipts, product-input fingerprint, fail-closed validation.

Raw output is kept under artifacts/evidence-runs. A receipt never upgrades site or product
acceptance. The fingerprint covers product inputs only (source, tests, build scripts, locks and
fixtures); documents are checked by check_docs.py and never make package or test evidence stale.
Plan id, schema version and the required test cases are read from the repository, not written here.
Run through scripts/stage.py.
"""
from __future__ import annotations
import hashlib
import json
import math
import os
import re
import subprocess
import time
from pathlib import Path
from benchmark_metrics import derive

ROOT=Path(__file__).resolve().parents[1]
MAP='docs/delivery/requirements-map.json'
RULE_EVIDENCE='docs/evidence/rule-evidence.json'
CASE_KINDS=('test','package')


def current_plan(root:Path=ROOT)->str:
    return read(root/MAP)['currentPlan']


def schema_version(root:Path=ROOT)->int:
    text=(root/'crates/domain/src/lib.rs').read_text(encoding='utf8')
    match=re.search(r'SCHEMA_VERSION:\s*i64\s*=\s*(\d+)\s*;',text)
    if not match:raise ValueError('schema version not found')
    return int(match[1])


def required_cases(root:Path=ROOT)->dict[str,list[str]]:
    """Test cases the current requirement scope needs, grouped by counter-example group."""
    groups:dict[str,list[str]]={}
    for row in read(root/MAP)['requirements']:
        scope=row.get('current',{})
        if not scope.get('applicable'):continue
        for name in scope.get('testCases',[]):
            for group in scope.get('caseGroups') or ['UNGROUPED']:
                if name not in groups.setdefault(group,[]):groups[group].append(name)
    return groups


def adapter_sites(root:Path=ROOT)->list[str]:
    """Site ids of the adapter crates in the workspace (adapters/<site> named trace-adapter-<site>)."""
    sites=[]
    for manifest in sorted((root/'adapters').glob('*/Cargo.toml')):
        match=re.search(r'^name\s*=\s*"trace-adapter-([a-z0-9]+)"',manifest.read_text(encoding='utf8'),re.M)
        if match:sites.append(match[1])
    return sites


def package_layout(root:Path=ROOT)->dict:
    """What a portable package must carry, derived from the workspace and the tool lock."""
    lock=read(root/'media-tools.lock.json')
    notices=[n['name'] for tool in lock.values() for n in tool.get('notices',[])]
    components=['trace-desktop.exe','trace-daemon.exe',*[f'adapters/trace-adapter-{site}.exe' for site in adapter_sites(root)]]
    media=['media/ffmpeg.exe','media/ffprobe.exe','media/ffplay.exe']
    tools=['tools/yt-dlp.exe','tools/deno.exe']
    legal=['LICENSE','adapters/douyin-NOTICE.txt','dependency-licenses.md','media/LICENSE.txt','media/SOURCE.txt','tools/SOURCE.txt',*[f'tools/{n}' for n in notices]]
    return {'components':components,'media':media,'tools':tools,'legal':legal,'executables':set(components)|set(media)|set(tools)}


def requirement_coverage(root:Path,cases:list[dict])->list[dict]:
    mapping=read(root/'docs/delivery/requirements-map.json')['requirements']
    passed={c['name'] for c in cases if c['status']=='passed'}
    result=[]
    for row in mapping:
        scope=row.get('current',{});applicable=scope.get('applicable',False)
        names=scope.get('testCases',[]);implementation=scope.get('implementation',[])
        status='passed' if applicable and names and set(names)<=passed and implementation else 'not-run'
        result.append({'id':row['id'],'applicable':applicable,'status':status,
                       'testCases':names if applicable else [],'implementation':implementation if applicable else []})
    return result


def validate_contract(bundle:dict,root:Path,cases:list[dict],expected:dict)->list[str]:
    errors=[]
    plan=current_plan(root);schema=schema_version(root)
    if plan not in (root/'docs/delivery/stage-plan.md').read_text(encoding='utf8'):errors.append('stage plan does not describe the current plan')
    if bundle.get('plan')!=plan:errors.append('wrong plan')
    if bundle.get('schemaVersion')!=schema:errors.append('wrong evidence schema version')
    coverage=requirement_coverage(root,cases)
    if not coverage or len({r['id'] for r in coverage})!=len(coverage) or not any(r['applicable'] for r in coverage):errors.append('incomplete requirement scope')
    for row in read(root/MAP)['requirements']:
        if row.get('current',{}).get('plan')!=plan:errors.append('missing current-plan mapping '+str(row.get('id')))
    if bundle.get('requirementCoverage')!=coverage:errors.append('requirements not bound to current mapping and cases')
    for row in coverage:
        if row['applicable'] and (row['status']!='passed' or any(p not in expected for p in row['implementation'])):errors.append('unproved requirement '+row['id'])
        if not row['applicable'] and row['status']!='not-run':errors.append('deferred requirement claimed complete')
    return errors


def sha(path:Path)->str:
    h=hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda:f.read(1<<20),b''):h.update(block)
    return h.hexdigest()

def inputs(root:Path=ROOT)->dict[str,str]:
    names=subprocess.check_output(['git','ls-files','-z','--cached','--others','--exclude-standard'],cwd=root).decode().split('\0')
    prefixes=('crates/','adapters/','apps/','tests/','scripts/','.cargo/')
    roots={'Cargo.toml','Cargo.lock','rust-toolchain.toml','package.json','pnpm-lock.yaml','pnpm-workspace.yaml','media-backend.lock.json','media-tools.lock.json','.env.example','LICENSE'}
    return {n:sha(root/n) for n in sorted(set(names)) if (n.startswith(prefixes) or n in roots) and (root/n).is_file()}

def contained(root:Path,name:str)->Path:
    """Evidence never follows absolute paths, parent traversal, links or junctions."""
    if not isinstance(name,str) or not name or '\\' in name or ':' in name:
        raise ValueError('invalid evidence path')
    path=Path(name)
    if path.is_absolute() or any(part in ('..','.','') for part in name.split('/')) or not path.parts:
        raise ValueError('evidence path outside workspace')
    current=root.resolve()
    for part in path.parts:
        current=current/part
        if current.is_symlink() or (hasattr(current,'is_junction') and current.is_junction()):
            raise ValueError('linked evidence path')
    if not current.resolve().is_relative_to(root.resolve()):raise ValueError('evidence path escapes workspace')
    return current

def inventory(folder:Path)->dict[str,str]:
    result={}
    for p in sorted(folder.rglob('*')):
        checked=contained(folder,p.relative_to(folder).as_posix())
        if checked.is_file():result[p.relative_to(folder).as_posix()]=sha(checked)
    return result

def read(path:Path)->dict:
    return json.loads(path.read_text(encoding='utf8'))

def validate_package(folder:Path,expected:dict,root:Path=ROOT)->list[str]:
    """Bind the package's own manifest, executable inventory and build receipts."""
    try:
        actual=inventory(folder);manifest=read(folder/'manifest.json');errors=[]
        sums={}
        for line in (folder/'SHA256SUMS.txt').read_text(encoding='utf8').splitlines():
            digest,name=line.split('  ',1);contained(folder,name)
            if name in sums:errors.append('duplicate package checksum')
            sums[name]=digest
        if sums!={n:h for n,h in actual.items() if n!='SHA256SUMS.txt'}:errors.append('package checksums do not cover actual files')
        layout=package_layout(root);components=set(layout['components'])
        if set(manifest['components'])!=components or len(manifest['components'])!=len(components):errors.append('incomplete package components')
        if manifest['built_binaries']!={n:actual[n] for n in components}:errors.append('package executable build hashes differ')
        if {n for n in actual if n.endswith('.exe')}!=layout['executables']:errors.append('extra or missing package executable')
        if manifest['sourceInputs']!=expected or manifest['source_files_sha256']!=expected:errors.append('package manifest source mismatch')
        if manifest['source_tree_sha256']!=hashlib.sha256(json.dumps(expected,sort_keys=True,separators=(',',':')).encode()).hexdigest():errors.append('package aggregate source mismatch')
        if manifest['stage']!=current_plan(root) or manifest['schema_version']!=schema_version(root) or manifest['productAcceptance']!='not-run':errors.append('package stage or acceptance mismatch')
        commands=manifest['build_commands']
        if {Path(c['argv'][0]).stem for c in commands}!={'cargo','corepack'}:errors.append('missing package compiler commands')
        if not any('trace-desktop/custom-protocol' in c['argv'] for c in commands):errors.append('missing embedded production frontend')
        for command in commands:
            if command['exitCode']!=0 or command['sourceInputs']!=expected or command['sourceUnchanged'] is not True:errors.append('failed or stale package build command')
            if sha(contained(root,command['log']))!=command['logSha256']:errors.append('missing or changed package build log')
        profile=read(folder/'profile.example.json')
        if profile['test_allowed_hosts'] or profile.get('extra_media_hosts') or profile.get('allow_media_path'):errors.append('package contains development network configuration')
        media=read(folder/'media/verified.json')
        if media['lock']!=read(root/'media-backend.lock.json'):errors.append('package media lock differs')
        for name,digest in media['files'].items():
            if sha(contained(folder,'media/'+name))!=digest:errors.append('package media file changed')
        if not {'ffmpeg.exe','ffprobe.exe','ffplay.exe'}<=set(media['files']):errors.append('missing verified media executables')
        tools=read(folder/'tools/verified.json');tools_lock=read(root/'media-tools.lock.json')
        if tools['lock']!=tools_lock:errors.append('package tool lock differs')
        for name,digest in tools['files'].items():
            if sha(contained(folder,'tools/'+name))!=digest:errors.append('package tool file changed')
        pinned={'yt-dlp.exe':tools_lock['yt_dlp']['sha256'],'deno.exe':tools_lock['deno']['member_sha256'],**{n['name']:n['sha256'] for tool in tools_lock.values() for n in tool.get('notices',[])}}
        for name,digest in pinned.items():
            if tools['files'].get(name)!=digest:errors.append(f'package tool {name} is not the pinned file')
        for name in layout['legal']:
            if name not in actual:errors.append('missing package legal material')
        if (root/'LICENSE').is_file() and actual.get('LICENSE')!=sha(root/'LICENSE'):errors.append('package project license mismatch')
        return errors
    except (AttributeError,KeyError,TypeError,ValueError,OSError):return ['invalid package manifest or build evidence']

def validate(bundle:dict,root:Path=ROOT,expected:dict|None=None,required:dict|None=None)->list[str]:
    try:return validate_contents(bundle,root,expected,required)
    except (AttributeError,KeyError,TypeError,IndexError,ValueError,OSError):return ['invalid evidence schema']


def validate_contents(bundle:dict,root:Path=ROOT,expected:dict|None=None,required:dict|None=None)->list[str]:
    errors=[];expected=inputs(root) if expected is None else expected
    required=required_cases(root) if required is None else required
    if bundle.get('sourceInputs')!=expected:errors.append('stale or incomplete source fingerprint')
    if bundle.get('productAcceptance')!='not-run':errors.append('automatic evidence cannot declare product acceptance')
    cases=bundle.get('cases',[]);by_name={}
    errors.extend(validate_contract(bundle,root,cases,expected))
    for case in cases:
        name=case.get('name');by_name.setdefault(name,[]).append(case)
        if case.get('status')!='passed':errors.append(f'failed or skipped case: {name}')
    for group,names in required.items():
        for name in names:
            found=by_name.get(name,[])
            if len(found)!=1:errors.append(f'{group}: missing or duplicate required case {name}')
    identities=[c.get('id',c.get('name')) for c in cases]
    if len(set(identities))!=len(identities):errors.append('duplicate case identifiers')
    receipts=bundle.get('commands',[])
    if not receipts:errors.append('missing command receipts')
    if {r.get('kind') for r in receipts}!={'test','bench','package','report'}:errors.append('missing evidence command kind')
    if len({r.get('id') for r in receipts})!=len(receipts):errors.append('duplicate command identifiers')
    recorded_cases=[case for r in receipts if r.get('kind') in CASE_KINDS for case in r.get('cases',[])]
    if cases!=recorded_cases:errors.append('case list not bound to command receipts')
    for r in receipts:
        if r.get('exitCode')!=0:errors.append(f'failed command {r.get("id")}')
        if r.get('sourceUnchanged') is not True:errors.append(f'command inputs changed {r.get("id")}')
        if r.get('sourceInputs')!=expected:errors.append(f'stale command {r.get("id")}')
        try:
            p=contained(root,r.get('log',''))
            if not p.is_file() or sha(p)!=r.get('logSha256'):errors.append(f'missing or altered raw log {r.get("id")}')
            elif r.get('kind') in CASE_KINDS and parse_cases(p.read_text(encoding='utf8',errors='replace'))!=r.get('cases'):errors.append(f'cases do not match raw log {r.get("id")}')
        except (ValueError,OSError):errors.append(f'invalid raw log path {r.get("id")}')
    package=bundle.get('package',{});declared=package.get('files',{})
    try:
        folder=contained(root,package.get('directory',''))
        actual=inventory(folder) if folder.is_dir() else {}
    except (ValueError,OSError):actual={};errors.append('invalid package path')
    if not declared or declared!=actual:errors.append('missing, extra or changed package member')
    layout=package_layout(root)
    for name in [*layout['components'],*layout['media'][:2],*layout['tools'],*layout['legal']]:
        if name not in actual:errors.append(f'package missing {name}')
    if package.get('sourceInputs')!=expected:errors.append('stale package inputs')
    if actual:errors.extend(validate_package(folder,expected,root))
    bench=bundle.get('benchmark',{})
    if bench.get('exitCode')!=0 or bench.get('status')!='passed':errors.append('failed or absent benchmark')
    if bench.get('sourceInputs')!=expected or bench.get('sourceUnchanged') is not True:errors.append('stale benchmark inputs')
    minimum={'durationSeconds':3600,'metadataEntries':100000,'mediaMetadata':500000,'bodyMedianChars':500,'ordinaryCheckTargets':100,'liveCheckTargets':20,'httpLatencyMs':100,'queryPageSize':50,'explorerEntries':100000,'streamBytesPerSecond':1000000,'parallelDownloads':2,'parallelRecordings':2,'recordingOnlyStreams':4,'mixedParallelSamples':1}
    for key,value in minimum.items():
        if not isinstance(bench.get(key),(int,float)) or bench[key]<value:errors.append(f'benchmark missing/short {key}')
    if set(bench.get('guiModes',[]))!={'open','closed','reconnected'}:errors.append('missing GUI lifecycle samples')
    for metric in ['privateWorkingSet','cpuAllProcesses','handles','queryP95Ms','queryP99Ms','throughputBytes','queueDepth','freeDiskBytes']:
        value=bench.get('measurements',{}).get(metric)
        if not isinstance(value,(int,float)) or isinstance(value,bool) or not math.isfinite(value) or value<0:errors.append(f'missing raw benchmark metric {metric}')
    try:raw=contained(root,bench.get('samples',''))
    except (ValueError,OSError):raw=root/'__missing__';errors.append('invalid samples path')
    if not raw.is_file() or sha(raw)!=bench.get('samplesSha256'):errors.append('missing or changed benchmark samples')
    else:
        try:
            raw_data=read(raw);samples=raw_data['samples'];times=[s['elapsedSeconds'] for s in samples]
            if raw_data.get('workload',{}).get('queryCacheState')!='new_core_connection_os_cache_unflushed':errors.append('missing cold-start connection evidence')
            recovery=raw_data.get('lifecycle',{}).get('recovery',{})
            if recovery.get('unfinishedOrdinaryTasks',0)<100 or recovery.get('serviceAccessible') is not True:errors.append('missing recovery workload and service evidence')
            derived=derive(raw_data)
            if any(bench.get(key)!=value for key,value in derived.items()):errors.append('benchmark summary does not match raw counters')
            if raw_data.get('sourceInputs')!=expected:errors.append('stale raw benchmark inputs')
            if raw_data.get('guiExecutableSha256')!=declared.get('trace-desktop.exe'):errors.append('benchmark GUI is not the packaged executable')
            probes=raw_data.get('lifecycle',{}).get('guiProbe',[])
            if len(probes)!=2 or any(p.get('embeddedPage') is not True or p.get('lifecycleControls') is not True or p.get('schemaVersion')!=schema_version(root) or p.get('acquisitionQueries') is not True or p.get('sourceQueries') is not True for p in probes) or len({p.get('instanceId') for p in probes})!=1:errors.append('missing packaged GUI page and reconnect verification')
            build=raw_data['build'];build_log=contained(root,build['log'])
            if build.get('exitCode')!=0 or sha(build_log)!=build['logSha256']:errors.append('invalid benchmark build receipt')
            if set(build.get('binaries',{}))!={'resource_bench.exe','bench_adapter.exe'}:errors.append('incomplete benchmark executable inventory')
            for name,digest in build.get('binaries',{}).items():
                binary=contained(root,'target/measure/'+name)
                if not binary.is_file() or sha(binary)!=digest:errors.append('benchmark executable changed after build')
            if derived['idleWindowSeconds']<900 or derived['recordingStarts']<4 or derived['jobErrors']!=0:errors.append('incomplete workload or lifecycle samples')
            if len(times)<721 or times[-1]-times[0]<3600 or any(b<=a or b-a>15 for a,b in zip(times,times[1:])):errors.append('insufficient continuous benchmark sample window')
            for sample in samples:
                if not sample.get('processes') or any(not isinstance(p.get('privateWorkingSet'),int) or p['privateWorkingSet']<0 for p in sample['processes']):errors.append('incomplete process resource sample');break
        except (ValueError,KeyError,TypeError,IndexError,OSError):errors.append('invalid benchmark sample schema')
    targets={'idleCpuMeanPct':1,'idleMemoryP95MiB':150,'hotQueryP95Ms':500,'coldQueryP95Ms':1500,'explorerHotP95Ms':500,'explorerColdP95Ms':1500,'recordingOnlyCpuMeanPct':5,'recordingOnlyMemoryP95MiB':300,'startRecordingP95Seconds':10,'recoverySeconds':30,'guiCloseSeconds':10}
    for metric,maximum in targets.items():
        value=bench.get('measurements',{}).get(metric)
        if not isinstance(value,(int,float)) or not math.isfinite(value) or value<0 or value>maximum:errors.append(f'unproved or failed NFR target {metric}')
    errors.extend(validate_rule_evidence(bundle.get('ruleEvidence'),root))
    return errors

def parse_cases(output:str)->list[dict]:
    clean=re.sub(r'\x1b\[[0-9;]*m','',output)
    cases=[];suite='unknown'
    for line in clean.splitlines():
        running=re.search(r'Running .*\((?:.*[/\\])?([^/\\]+?)-[0-9a-f]+(?:\.exe)?\)',line)
        if running:suite=running[1]
        case=re.match(r'^test (\S+) \.\.\. (ok|FAILED|ignored)',line)
        if case:
            cases.append({'id':f'rust:{suite}:{case[1]}','name':case[1].split('::')[-1],'status':'passed' if case[2]=='ok' else 'failed'})
            continue
        case=re.match(r'^(test_\w+) \(([^)]+)\) \.\.\. (ok|FAIL|ERROR|skipped)',line)
        if case:
            cases.append({'id':f'python:{case[2]}','name':case[1],'status':'passed' if case[3]=='ok' else 'failed'})
            continue
        case=re.match(r'^\s*([✓✔×✗])\s+(.+?) > (.+?)\s+\d+(?:\.\d+)?ms\s*$',line)
        if case:
            # Vitest includes every describe() group after the file name. Match
            # required leaf names while preserving the full scoped identity.
            cases.append({'id':f'vitest:{case[2]}:{case[3]}','name':case[3].rsplit(' > ',1)[-1].strip(),'status':'passed' if case[1] in ('✓','✔') else 'failed'})
    return cases

def run_receipt(kind:str,receipt_id:str,output:Path,command:list[str],cwd:Path=ROOT)->int:
    """Run `command`, keep its raw log next to the receipt and fingerprint the inputs around it."""
    before=inputs();out=Path(output).resolve();out.parent.mkdir(parents=True,exist_ok=True)
    log=out.with_suffix('.log');start=time.time()
    if out.exists() or log.exists():raise ValueError('choose a new receipt path; prior failures are preserved')
    with log.open('w',encoding='utf8') as f:
        env=dict(os.environ,NO_COLOR='1',FORCE_COLOR='0',PYTHONIOENCODING='utf-8')
        p=subprocess.run(command,cwd=cwd,stdout=f,stderr=subprocess.STDOUT,text=True,encoding='utf8',env=env)
    cases=parse_cases(log.read_text(encoding='utf8',errors='replace'))
    data={'id':receipt_id,'kind':kind,'argv':command,'startedUnix':start,'durationSeconds':time.time()-start,'exitCode':p.returncode,'sourceInputs':before,'sourceUnchanged':before==inputs(),'cases':cases,'log':log.relative_to(ROOT).as_posix(),'logSha256':sha(log)}
    out.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n',encoding='utf8')
    return p.returncode if data['sourceUnchanged'] else 1

def rule_evidence_digest(root:Path=ROOT)->dict:
    path=root/RULE_EVIDENCE
    return {'path':RULE_EVIDENCE,'sha256':sha(path)}


def validate_rule_evidence(reference:dict|None,root:Path=ROOT)->list[str]:
    """The public record of what was checked on the real sites; a rule is only as implemented as it says."""
    from rule_evidence import check_file
    if not isinstance(reference,dict) or reference.get('path')!=RULE_EVIDENCE:return ['missing rule evidence reference']
    path=root/RULE_EVIDENCE
    if not path.is_file():return ['missing rule evidence file']
    errors=[]
    if reference.get('sha256')!=sha(path):errors.append('rule evidence changed after the report')
    errors.extend(check_file(path,root))
    return errors


def assemble(receipts:list[dict],package:Path,benchmark:dict,output:Path)->list[str]:
    """Bind receipts, package, benchmark and the rule evidence into one validated report."""
    folder=Path(package).resolve();manifest=read(folder/'manifest.json')
    cases=[c for r in receipts if r.get('kind') in CASE_KINDS for c in r.get('cases',[])]
    bundle={'plan':current_plan(),'schemaVersion':schema_version(),'requirementCoverage':requirement_coverage(ROOT,cases),'sourceInputs':inputs(),'productAcceptance':'not-run',
        'commands':receipts,'cases':cases,
        'package':{'directory':folder.relative_to(ROOT).as_posix(),'sourceInputs':manifest.get('sourceInputs'),
            'files':{p.relative_to(folder).as_posix():sha(p) for p in sorted(folder.rglob('*')) if p.is_file()}},'benchmark':benchmark,
        'ruleEvidence':rule_evidence_digest()}
    errors=validate(bundle)
    bundle['status']='failed' if errors else 'passed';bundle['errors']=errors
    output=Path(output);output.parent.mkdir(parents=True,exist_ok=True);output.write_text(json.dumps(bundle,ensure_ascii=False,indent=2)+chr(10),encoding='utf8')
    return errors
