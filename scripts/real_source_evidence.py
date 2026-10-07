#!/usr/bin/env python3
"""Bounded, explicitly authorized AC-29/30 through a current production package.

The driver never downloads media. It imports the selected local credential via
Core IPC, drives the packaged GUI's source selections, waits for Core objects,
then checks offline reads, export and complete local decoding. No CI execution.
"""
from __future__ import annotations
import argparse,base64,hashlib,json,os,sqlite3,subprocess,sys,time
from contextlib import closing
from datetime import datetime,timezone
from pathlib import Path
import stage_evidence as evidence
from check_package import Reader,read_pipe_frame,write_frame
from prepare_site_check import prepare,unlinked
from real_chain_gate import authorized_budget
from webview_probe import Desktop
ROOT=evidence.ROOT
FLAGS=subprocess.CREATE_NO_WINDOW if os.name=='nt' else 0

def relative(path):return Path(path).resolve().relative_to(ROOT).as_posix()
def save(path,value):Path(path).write_text(json.dumps(value,ensure_ascii=False,indent=2)+'\n',encoding='utf8')
def rows(path,sql,args=()):
    with closing(sqlite3.connect(Path(path).as_uri()+'?mode=ro',uri=True)) as db:
        db.row_factory=sqlite3.Row;return [dict(r) for r in db.execute(sql,args)]

class Session:
    def __init__(self,package,profile):
        self.package=package;self.profile=profile;self.pipe=None
        env=dict(os.environ);env.pop('TRACE_PROFILE',None);env.pop('TRACE_DAEMON_PATH',None)
        self.proc=subprocess.Popen([str(package/'trace-daemon.exe'),'start','--profile',str(profile)],stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,env=env,creationflags=FLAGS)
        ready=Reader(self.proc.stdout,lines=True).get(30)
        if ready.get('status')!='ready':raise ValueError('Core startup failed')
        for _ in range(40):
            try:self.pipe=open(ready['pipe'],'r+b',buffering=0);break
            except OSError:time.sleep(.1)
        if self.pipe is None:raise ValueError('Core pipe unavailable')
        archive=json.loads(profile.read_text(encoding='utf8'))['archive_id']
        write_frame(self.pipe,{'client':'authorized-source-verification','protocol_version':'0.1','archive_id':archive})
        if read_pipe_frame(self.pipe).get('frame')!='hello':raise ValueError('Core handshake failed')
    def call(self,command,args=None):
        rid=str(time.time_ns());write_frame(self.pipe,{'protocol_version':'0.1','request_id':rid,'command':command,'idempotency_key':rid,'args':args or {}})
        while True:
            result=read_pipe_frame(self.pipe,40)
            if result.get('request_id')==rid:
                if not result.get('ok'):raise ValueError('Core command rejected: '+result.get('error',{}).get('code','unknown'))
                return result['result']
    def stop(self):
        if self.proc.poll() is None:
            try:self.call('daemon.shutdown')
            except (OSError,ValueError,TimeoutError):pass
        if self.pipe:self.pipe.close();self.pipe=None
        try:self.proc.wait(timeout=30)
        except subprocess.TimeoutExpired:self.proc.kill();self.proc.wait(timeout=10)

def gui(window,root,action):
    index=str(time.time_ns());source=root/f'gui-{index}-input.json';output=root/f'gui-{index}-result.json';save(source,action)
    result=subprocess.run(['node',str(ROOT/'scripts/source_webview.mjs'),str(window.port),str(source),str(output)],capture_output=True,timeout=105,creationflags=FLAGS)
    (root/f'gui-{index}.log').write_bytes(result.stdout+b'\n'+result.stderr)
    if result.returncode:raise ValueError('packaged source GUI failed; local diagnostic retained')
    window.assert_background();return evidence.read(output)

def wait_request(session,rid,seconds=900):
    deadline=time.monotonic()+seconds
    while time.monotonic()<deadline:
        budget=session.call('daemon.status').get('acquisition_budget')
        if not budget or budget['stopped']:raise ValueError('bounded site run stopped at access or transport failure')
        row=next((r for r in session.call('source.list')['items'] if r['request_id']==rid),None)
        if row and row['state']=='succeeded':return row
        if row and row['state'] in ('needs_attention','cancelled'):raise ValueError('source task requires attention; stopping this run')
        time.sleep(1)
    raise ValueError('source task deadline')

def local_decode(package,path,root,name):
    log=root/f'{name}-decode.log'
    command=[str(package/'media/ffmpeg.exe'),'-hide_banner','-v','error','-xerror','-threads','2','-protocol_whitelist','file,pipe','-i',str(path),'-map','0:v?','-map','0:a?','-f','null','-']
    with log.open('wb') as stream:
        result=subprocess.run(command,stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=stream,timeout=900,creationflags=FLAGS)
    if result.returncode:raise ValueError('complete offline decode failed')
    return {'input':relative(path),'inputSha256':evidence.sha(path),'inputBytes':path.stat().st_size,'log':relative(log),'logSha256':evidence.sha(log),'exitCode':result.returncode,'fullFile':True,'networkInputs':0}

def collect(package,root,profile,dbpath,request,kind,site,export_dir):
    rid=request['requestId'];record=rows(dbpath,'SELECT * FROM source_requests WHERE request_id=?',(rid,))[0]
    tasks=rows(dbpath,"SELECT job_id,job_type,state FROM jobs WHERE json_extract(payload,'$.source_request_id')=?",(rid,))
    if any(t['state']!='succeeded' for t in tasks):raise ValueError('Core source tasks are incomplete')
    entries=rows(dbpath,"SELECT m.media_id,m.role,v.variant_id,v.blob_id,b.size,v.source_representation,v.measured_spec FROM media_items m JOIN current_entry_revisions r ON r.revision_id=m.revision_id JOIN asset_selections s ON s.media_id=m.media_id JOIN media_variants v ON v.variant_id=s.variant_id JOIN blobs b ON b.blob_id=v.blob_id WHERE r.entry_id=? AND m.required=1",(record['entry_id'],))
    required=rows(dbpath,"SELECT m.media_id FROM media_items m JOIN current_entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=? AND m.required=1",(record['entry_id'],))
    if not required or {r['media_id'] for r in required}!={r['media_id'] for r in entries}:raise ValueError('required resources incomplete')
    objects=[];decodes=[]
    for index,row in enumerate(entries):
        sha=row['blob_id'].removeprefix('sha256:');path=Path(profile['archive_root'])/'objects/sha256'/sha[:2]/sha
        if path.stat().st_size!=row['size'] or evidence.sha(path)!=sha:raise ValueError('Core object failed hash validation')
        objects.append({'mediaId':row['media_id'],'variantId':row['variant_id'],'path':relative(path),'sha256':sha,'bytes':row['size'],'role':row['role'],'actualSpec':json.loads(row['measured_spec'] or '{}')})
        decodes.append(local_decode(package,path,root,f'{kind}-required-{index}'))
    playback=None
    if kind=='video':
        derivatives=rows(dbpath,"SELECT v.blob_id FROM media_variants v JOIN media_items m ON m.media_id=v.media_id JOIN current_entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=? AND v.source_representation='playback-mux-v1'",(record['entry_id'],))
        if derivatives:
            sha=derivatives[-1]['blob_id'].removeprefix('sha256:');playback=Path(profile['archive_root'])/'objects/sha256'/sha[:2]/sha
        else:playback=Path(ROOT/objects[0]['path'])
    else:
        playable=next((o for o in objects if o['role']=='recording' and o['actualSpec'].get('codec')),None)
        if not playable:raise ValueError('no sealed playable live fragment')
        playback=ROOT/playable['path']
    probe=root/f'{kind}-probe.json'
    result=subprocess.run([str(package/'media/ffprobe.exe'),'-v','error','-protocol_whitelist','file,pipe','-show_streams','-show_format','-of','json',str(playback)],capture_output=True,timeout=90,creationflags=FLAGS)
    if result.returncode:raise ValueError('offline probe failed')
    probe.write_bytes(result.stdout);data=json.loads(result.stdout);decodes.append(local_decode(package,playback,root,f'{kind}-playback'))
    streams=data['streams'];video=next((s for s in streams if s.get('codec_type')=='video'),None)
    if not video or not any(s.get('codec_type')=='audio' for s in streams):raise ValueError('necessary video/audio missing')
    if site=='bilibili' and kind=='video' and (video['width'],video['height'])!=(3840,2160):raise ValueError('specified video is not actual 4K')
    snapshot=json.loads(record['snapshot']);source_duration=snapshot.get('source_duration_seconds') or 0
    # source duration fallback is rejected by the final gate if the source did
    # not declare duration; normal adapters preserve their source metadata.
    fragment_seconds=0
    if kind=='live':
        if min(video['width'],video['height'])<720:raise ValueError('actual recording below 720P')
        fragments=rows(dbpath,"SELECT start_ms,end_ms,state FROM live_fragments WHERE session_id=? AND state='sealed'",(record['session_id'],))
        fragment_seconds=sum(max(0,(f['end_ms'] or 0)-(f['start_ms'] or 0)) for f in fragments)/1000
        if fragment_seconds<60:raise ValueError('less than 60 seconds of sealed source timestamps')
        if rows(dbpath,'SELECT state FROM live_sessions WHERE session_id=?',(record['session_id'],))[0]['state']!='sealed':raise ValueError('recording not sealed')
    exports=[]
    for obj in objects:
        copies=list(export_dir.rglob(obj['sha256']+'*'))
        copy=next((p for p in copies if p.is_file() and evidence.sha(p)==obj['sha256']),None)
        if not copy:raise ValueError('exported object is missing or changed')
        exports.append({'path':relative(copy),'sha256':obj['sha256'],'bytes':obj['bytes']})
    return {'requestId':rid,'platform':site,'kind':kind,'selectedIds':request['selectedIds'],'taskIds':[t['job_id'] for t in tasks],'guiSelection':request['guiSelection'],'offlineRead':True,'offlineReads':request['offlineReads'],'reopened':True,'guiClosedDuringCapture':kind=='live','lifecycle':request.get('lifecycle',{}),'sealed':kind=='live','exported':True,'exports':exports,'receivedSeconds':fragment_seconds,'sourceDurationSeconds':source_duration,'objects':objects,'probe':{'path':relative(probe),'sha256':evidence.sha(probe),'input':relative(playback),'inputSha256':evidence.sha(playback)},'decodes':decodes}

def run(args):
    package=Path(args.package).resolve();root=(ROOT/args.out).resolve();case_file=(ROOT/args.cases).resolve();unlinked(case_file)
    if not case_file.is_relative_to(ROOT/'.local/site-check'):raise ValueError('case file must be local')
    cases=evidence.read(case_file);names=[args.site+'Video',args.site+'Live']
    for name in names:
        from urllib.parse import urlsplit
        u=urlsplit(cases[name]);expected=('www.bilibili.com','live.bilibili.com') if args.site=='bilibili' else ('www.douyin.com','live.douyin.com')
        if u.scheme!='https' or u.hostname not in expected or u.username or u.password:raise ValueError('authorized object input differs')
    source=evidence.inputs();errors=evidence.validate_package(package,source)
    if errors:raise ValueError('current package evidence is required')
    authorized=authorized_budget(ROOT,args.site)
    prior=[];metadata_used=0;media_used=0
    for previous in args.previous_run:
        previous=(ROOT/previous).resolve();unlinked(previous)
        if not previous.is_relative_to(ROOT/'.local/site-check') or evidence.read(previous/'request.json')['platform']!=args.site:raise ValueError('previous run scope differs')
        file=previous/'data/state/acquisition-budget.json';path=relative(file)
        if any(r['path']==path for r in prior):raise ValueError('duplicate previous budget')
        budget=evidence.read(file);metadata_used+=sum(r.get('metadata',True) for r in budget['attempts']);media_used+=max(budget.get('media_bytes',0),budget.get('media_reserved',0))
        prior.append({'path':path,'sha256':evidence.sha(file)})
    if metadata_used>=authorized['metadata_requests'] or media_used>=authorized['media_bytes']:raise ValueError('authorized cumulative budget exhausted')
    prepare(ROOT,args.site,args.out,relative(package))
    if not args.execute_authorized:return {'status':'prepared','networkRequests':0,'executionAuthorized':False}
    profile=evidence.read(root/'profile.json');profile.update(media_dir=str(package/'media'),recording_fragment_seconds=300,recording_fragment_bytes=512*1024*1024,metadata_jitter_ms=0,acquisition_limits={'metadata_requests':authorized['metadata_requests']-metadata_used,'media_bytes':authorized['media_bytes']-media_used,'interval_ms':2000})
    save(root/'profile.json',profile);dbpath=Path(profile['data_root'])/'state/catalog.sqlite3';session=None;windows=[];results=[]
    try:
        session=Session(package,root/'profile.json')
        cookie_path=evidence.read(root/'request.json')['credential_file'];secret=Path(cookie_path).read_text(encoding='utf-8-sig').strip()
        imported=session.call('credential.import',{'platform':args.site,'subject':None,'secret':secret});del secret
        credential=imported['credential_id'];deadline=time.monotonic()+45
        while time.monotonic()<deadline:
            values=session.call('credential.list',{'platform':args.site})['items'];identity=next(c for c in values if c['credential_id']==credential)
            if identity['status'] in ('valid','authenticated'):break
            if identity['status']=='invalid':raise ValueError('credential was explicitly rejected')
            if session.call('daemon.status')['acquisition_budget']['stopped']:raise ValueError('authentication run stopped')
            time.sleep(1)
        else:raise ValueError('authentication could not be established')
        window=Desktop(package/'trace-desktop.exe',root/'profile.json',root/'gui-first');windows.append(window)
        for kind in ['live','video']:
            request=gui(window,root,{'mode':'acquire','url':cases[args.site+kind.title()],'credentialId':credential,**({'width':3840,'height':2160,'codec':'avc'} if args.site=='bilibili' and kind=='video' else {'width':1080,'height':1920} if kind=='video' else {'quality':'10000-avc' if args.site=='bilibili' else 'FULL_HD1'})})
            results.append((kind,request));print(json.dumps({'site':args.site,'phase':kind+'-confirmed'}),flush=True)
            if kind=='live':
                deadline=time.monotonic()+45
                while time.monotonic()<deadline:
                    current=next(r for r in session.call('source.list')['items'] if r['request_id']==request['requestId'])
                    if current['state']=='needs_attention':raise ValueError('live source task requires attention')
                    if current.get('session_id'):
                        fragments=rows(dbpath,'SELECT captured_at FROM live_fragments WHERE session_id=? ORDER BY captured_at LIMIT 1',(current['session_id'],))
                        if fragments and fragments[0]['captured_at']:break
                    if session.call('daemon.status')['acquisition_budget']['stopped']:raise ValueError('live source run stopped')
                    time.sleep(.5)
                else:raise ValueError('live capture did not begin')
                captured=datetime.fromisoformat(fragments[0]['captured_at'].replace('Z','+00:00')).timestamp()
                initial_instance=session.call('daemon.status')['instance_id'];closed=time.time()
                window.action('close');window.wait(10)
                while time.time()<captured+65:
                    if session.proc.poll() is not None:raise ValueError('GUI close stopped Core')
                    if session.call('daemon.status')['acquisition_budget']['stopped']:raise ValueError('recording budget stopped')
                    time.sleep(1)
                continued=session.call('daemon.status')
                if continued['instance_id']!=initial_instance:raise ValueError('Core instance changed during GUI close')
                window=Desktop(package/'trace-desktop.exe',root/'profile.json',root/'gui-reconnected');windows.append(window)
                stop=gui(window,root,{'mode':'stop','requestId':request['requestId']})
                request['lifecycle']={'coreInstanceId':initial_instance,'continuedCoreInstanceId':continued['instance_id'],'captureStartedUnix':captured,'guiClosedUnix':closed,'guiReopenedUnix':time.time(),'guiStop':stop['guiStop']}
            completed=wait_request(session,request['requestId']);request['entryId']=completed['entry_id']
            print(json.dumps({'site':args.site,'phase':kind+'-core-complete'}),flush=True)
        export_dir=root/'export';session.call('export.start',{'target_dir':str(export_dir),'include_media':True,'creator_id':None})
        deadline=time.monotonic()+180
        while time.monotonic()<deadline:
            exported=rows(dbpath,"SELECT state FROM jobs WHERE job_type='export' ORDER BY created_at DESC LIMIT 1")
            if exported and exported[0]['state']=='succeeded':break
            if exported and exported[0]['state'] in ('failed','cancelled'):raise ValueError('Core export failed')
            time.sleep(.5)
        else:raise ValueError('Core export deadline')
        network=session.call('daemon.status')['acquisition_budget'];window.action('close');window.wait(10);session.stop();session=None
        session=Session(package,root/'profile.json');window=Desktop(package/'trace-desktop.exe',root/'profile.json',root/'gui-offline');windows.append(window)
        for kind,request in results:
            gui(window,root,{'mode':'read','entryId':request['entryId']})
            request['offlineReads']=[]
            for row in rows(dbpath,"SELECT v.blob_id,b.size FROM media_variants v JOIN asset_selections s ON s.variant_id=v.variant_id JOIN blobs b ON b.blob_id=v.blob_id JOIN media_items m ON m.media_id=v.media_id JOIN current_entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=? AND m.required=1",(request['entryId'],)):
                lease=session.call('media.lease',{'blob_id':row['blob_id']})
                length=min(row['size'],65536);chunk=session.call('media.read_range',{'lease_id':lease['lease_id'],'offset':0,'length':length})
                content=base64.b64decode(chunk['data_b64'],validate=True);digest=row['blob_id'].removeprefix('sha256:')
                path=Path(profile['archive_root'])/'objects/sha256'/digest[:2]/digest
                with path.open('rb') as file:expected=file.read(length)
                if chunk['offset']!=0 or chunk['lease_id']!=lease['lease_id'] or len(content)!=length or content!=expected:raise ValueError('offline Core read differs from verified object')
                request['offlineReads'].append({'blobId':row['blob_id'],'offset':0,'bytes':length,'sha256':hashlib.sha256(content).hexdigest()})
                session.call('media.release',{'lease_id':lease['lease_id']})
        after=session.call('daemon.status')['acquisition_budget']
        if after['request_count']!=network['request_count']:raise ValueError('offline read made new source requests')
        window.action('close');window.wait(10);session.stop();session=None
        snapshot=root/'catalog-snapshot.sqlite3'
        with closing(sqlite3.connect(dbpath.as_uri()+'?mode=ro',uri=True)) as live,closing(sqlite3.connect(snapshot)) as target:live.backup(target)
        items=[collect(package,root,profile,snapshot,request,kind,args.site,export_dir) for kind,request in results]
        result={'origin':'packaged_core_gui','status':'passed','sourceInputs':source,'sourceUnchanged':source==evidence.inputs(),'packageManifestSha256':evidence.sha(package/'manifest.json'),'database':{'path':relative(snapshot),'sha256':evidence.sha(snapshot)},'priorRuns':prior,'authorizedBudget':authorized,'networkBudget':network,'cases':items}
        if not result['sourceUnchanged']:raise ValueError('source inputs changed during real run')
        save(root/'evidence.json',result)
        for item in items:
            name=f"c14_real_{args.site}_{item['kind']}";print(f'{name} (real_product_chain.RealSources.{name}) ... ok',flush=True)
        return {'status':'passed','site':args.site,'evidence':relative(root/'evidence.json'),'metadataRequests':network['metadata_request_count'],'mediaBytes':network['media_received_bytes']}
    except Exception as exc:
        failure={'status':'failed','reason':str(exc) if isinstance(exc,ValueError) else type(exc).__name__,'sourceInputs':source,'sourceUnchanged':source==evidence.inputs(),'priorRuns':prior,'authorizedBudget':authorized}
        if session:
            try:failure['networkBudget']=session.call('daemon.status').get('acquisition_budget')
            except Exception:pass
        save(root/'failure.json',failure);raise ValueError(failure['reason']) from None
    finally:
        for window in windows:window.dispose()
        if session:session.stop()

def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--site',required=True,choices=['bilibili','douyin']);parser.add_argument('--package',required=True);parser.add_argument('--cases',required=True);parser.add_argument('--out',required=True);parser.add_argument('--previous-run',action='append',default=[]);parser.add_argument('--execute-authorized',action='store_true');args=parser.parse_args()
    try:result=run(args)
    except Exception as error:print(json.dumps({'status':'failed','reason':str(error) if isinstance(error,ValueError) else type(error).__name__}));return 1
    print(json.dumps(result));return 0
if __name__=='__main__':sys.exit(main())
