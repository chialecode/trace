"""Read-only validation of AC-29/30 receipts, Core catalogs and local media."""
from __future__ import annotations
import hashlib,json,re,sqlite3
from contextlib import closing
from pathlib import Path

def authorized_budget(root:Path,site:str)->dict:
    """Read the current user-authorized ceiling, never a receipt's own claim."""
    markers=re.findall(r'<!-- ACT-002-AUTHORIZED-BUDGETS (\{[^\r\n]+\}) -->',(root/'USER-ACTIONS.md').read_text(encoding='utf8'))
    if len(markers)!=1:raise ValueError('missing or ambiguous ACT-002 budget')
    values=json.loads(markers[0])
    if set(values)!={'bilibili','douyin'} or site not in values:raise ValueError('invalid ACT-002 site budget')
    for budget in values.values():
        if not isinstance(budget,dict) or set(budget)!={'metadata_requests','media_bytes'} or any(type(n) is not int or n<=0 for n in budget.values()):raise ValueError('invalid ACT-002 budget')
    return values[site]

def validate_real(references:list,root:Path,expected:dict,package:dict)->list[str]:
    from stage_evidence import contained,read,sha
    errors=[];seen=set()
    if not isinstance(references,list) or not references:return ['missing real packaged Core product-chain evidence']
    for reference in references:
        try:
            path=contained(root,reference['path'])
            if sha(path)!=reference['sha256']:raise ValueError('real receipt changed')
            data=read(path)
            if data.get('origin')!='packaged_core_gui' or data.get('status')!='passed' or data.get('sourceInputs')!=expected or data.get('sourceUnchanged') is not True:raise ValueError('diagnostic or stale real run')
            if data.get('packageManifestSha256')!=package.get('files',{}).get('manifest.json'):raise ValueError('real run package mismatch')
            db_path=contained(root,data['database']['path'])
            if sha(db_path)!=data['database']['sha256']:raise ValueError('real catalog changed')
            with closing(sqlite3.connect(db_path.as_uri()+'?mode=ro',uri=True)) as db:
                for item in data['cases']:
                    site,kind=item['platform'],item['kind'];key=(site,kind)
                    if key in seen or site not in ('bilibili','douyin') or kind not in ('video','live'):raise ValueError('real case scope')
                    seen.add(key)
                    validate_case(item,data,db,root,contained,sha,read)
        except (OSError,ValueError,KeyError,TypeError,IndexError,sqlite3.Error) as error:
            errors.append('invalid real product chain: '+str(error) if isinstance(error,ValueError) else 'invalid real product chain schema or local evidence')
    if seen!={(s,k) for s in ('bilibili','douyin') for k in ('video','live')}:errors.append('missing one of four real product cases')
    return errors

def validate_case(item,data,db,root,contained,sha,read):
    site,kind=item['platform'],item['kind'];rid=item['requestId']
    row=db.execute('SELECT r.platform,r.kind,r.entry_id,r.authentication,r.credential_generation,r.snapshot,r.selected,s.acquisition_kind,r.scope_id,r.credential_id,r.validated_at,r.session_id FROM source_requests r JOIN subscriptions s ON s.subscription_id=r.scope_id WHERE r.request_id=?',(rid,)).fetchone()
    if not row or row[0:2]!=(site,kind) or row[3]!='authenticated' or row[4] is None or row[7]!='one_shot' or not row[9] or not row[10]:raise ValueError('real request or identity missing')
    if item.get('guiSelection') is not True or not item.get('selectedIds') or set(item['selectedIds'])!={x['id'] for x in json.loads(row[6])}:raise ValueError('real selection not bound to Core')
    if db.execute("SELECT count(*) FROM jobs WHERE job_type='scan' AND (subject_id=? OR json_extract(payload,'$.subscription_id')=?)",(row[8],row[8])).fetchone()[0]:raise ValueError('real run enumerated an author')
    tasks=db.execute("SELECT job_id,job_type,state FROM jobs WHERE json_extract(payload,'$.source_request_id')=?",(rid,)).fetchall()
    if not tasks or {t[0] for t in tasks}!=set(item['taskIds']) or any(t[2]!='succeeded' for t in tasks):raise ValueError('real Core tasks not complete')
    if not {'source_acquire','media' if kind=='video' else 'record'}<={t[1] for t in tasks}:raise ValueError('real Core acquisition missing')
    required=db.execute("SELECT m.media_id FROM media_items m JOIN current_entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=? AND m.required=1",(row[2],)).fetchall()
    objects=item.get('objects',[])
    if not required or not {r[0] for r in required}<={o['mediaId'] for o in objects}:raise ValueError('missing necessary object hashes')
    for obj in objects:
        materialized=db.execute("SELECT b.size,v.blob_id,x.auth_state,x.credential_generation,mz.job_id FROM media_variants v JOIN asset_selections s ON s.variant_id=v.variant_id AND s.media_id=v.media_id JOIN blobs b ON b.blob_id=v.blob_id JOIN resource_acquisitions a ON a.variant_id=v.variant_id JOIN acquisition_contexts x ON x.context_id=a.context_id JOIN materializations mz ON mz.subject_id=v.media_id AND mz.blob_id=v.blob_id AND mz.stage='COMMITTED' AND mz.job_id=x.job_id AND mz.generation=x.job_generation WHERE v.variant_id=? AND v.media_id=? AND a.outcome='acquired' AND x.credential_id=? AND x.validated_at IS NOT NULL AND v.approval_state IN ('automatic','accepted','kept')",(obj['variantId'],obj['mediaId'],row[9])).fetchall()
        if not any(r[0]==obj['bytes'] and r[1]=='sha256:'+obj['sha256'] and r[2]=='authenticated' and r[3]==row[4] and r[4] in item['taskIds'] for r in materialized):raise ValueError('object lacks same-scope acquisition receipt')
        path=contained(root,obj['path'])
        if path.stat().st_size!=obj['bytes'] or sha(path)!=obj['sha256']:raise ValueError('real object bytes differ')
        with path.open('rb') as stream:prefix=stream.read(min(65536,obj['bytes']))
        if not any(r.get('blobId')=='sha256:'+obj['sha256'] and r.get('offset')==0 and r.get('bytes')==len(prefix) and r.get('sha256')==hashlib.sha256(prefix).hexdigest() for r in item.get('offlineReads',[])):raise ValueError('missing verified Core range read')
        copy=next((e for e in item.get('exports',[]) if e.get('sha256')==obj['sha256'] and e.get('bytes')==obj['bytes']),None)
        if not copy:raise ValueError('missing object export')
        exported=contained(root,copy['path'])
        if exported.stat().st_size!=obj['bytes'] or sha(exported)!=obj['sha256']:raise ValueError('exported object differs')
    if item.get('offlineRead') is not True or item.get('reopened') is not True:raise ValueError('missing offline reopen/read')
    probe_file=contained(root,item['probe']['path'])
    if sha(probe_file)!=item['probe']['sha256']:raise ValueError('media probe changed')
    if sha(contained(root,item['probe']['input']))!=item['probe']['inputSha256']:raise ValueError('probe input changed')
    probe=read(probe_file);video=next((s for s in probe.get('streams',[]) if s.get('codec_type')=='video'),None)
    if not video or not any(s.get('codec_type')=='audio' for s in probe['streams']):raise ValueError('missing necessary video/audio track')
    if kind=='live':
        if min(video.get('width',0),video.get('height',0))<720:raise ValueError('actual live resolution below 720P')
        if item.get('receivedSeconds',0)<60 or item.get('guiClosedDuringCapture') is not True or item.get('sealed') is not True or item.get('exported') is not True:raise ValueError('incomplete live capture lifecycle')
        session=db.execute('SELECT state,manual_stopped,ended_at FROM live_sessions WHERE session_id=?',(row[11],)).fetchone()
        fragments=db.execute('SELECT f.media_id,f.state,f.start_ms,f.end_ms,m.required FROM live_fragments f LEFT JOIN media_items m ON m.media_id=f.media_id WHERE f.session_id=?',(row[11],)).fetchall()
        playable=[f for f in fragments if f[4]==1]
        actual_seconds=sum(max(0,(f[3] or 0)-(f[2] or 0)) for f in playable if f[1]=='sealed')/1000
        # Core also retains exact raw captures, marked source/required=0; they
        # are provenance, not additional playback time or undecoded fragments.
        invalid_fragment=any((f[1]!='sealed' or f[0] not in {o['mediaId'] for o in objects}) if f[4]==1 else (f[1]!='source' or f[4]!=0) for f in fragments)
        if not session or session[0:2]!=('sealed',1) or not session[2] or not playable or invalid_fragment or actual_seconds<60 or abs(actual_seconds-item['receivedSeconds'])>.001:raise ValueError('Core fragments do not prove complete sealed capture')
        lifecycle=item.get('lifecycle',{})
        if not lifecycle.get('coreInstanceId') or lifecycle.get('coreInstanceId')!=lifecycle.get('continuedCoreInstanceId') or lifecycle.get('guiStop') is not True or lifecycle.get('guiReopenedUnix',0)-lifecycle.get('captureStartedUnix',0)<60 or lifecycle.get('guiReopenedUnix',0)-lifecycle.get('guiClosedUnix',0)<30:raise ValueError('Core continuation while GUI closed is unproved')
    elif site=='bilibili' and (video.get('width'),video.get('height'))!=(3840,2160):raise ValueError('specified Bilibili video is not 4K')
    duration=float(probe.get('format',{}).get('duration',0))
    if duration<=0:raise ValueError('unknown saved duration')
    if kind=='video':
        expected_duration=item.get('sourceDurationSeconds',0)
        if expected_duration!=json.loads(row[5]).get('source_duration_seconds') or expected_duration<=0 or duration+1<expected_duration*.97:raise ValueError('incomplete source video duration')
    decoded=set()
    for decode in item['decodes']:
        log=contained(root,decode['log'])
        if decode.get('exitCode')!=0 or sha(log)!=decode.get('logSha256') or decode.get('fullFile') is not True or decode.get('networkInputs')!=0:raise ValueError('missing complete offline decode')
        file=contained(root,decode['input'])
        if file.stat().st_size!=decode['inputBytes'] or sha(file)!=decode['inputSha256']:raise ValueError('decode input changed')
        decoded.add(decode['inputSha256'])
    if not {o['sha256'] for o in objects}|{item['probe']['inputSha256']}<=decoded:raise ValueError('missing complete decode of required inputs')
    budget=data['networkBudget']
    prior_metadata=0;prior_media=0;prior_paths=set()
    for previous in data.get('priorRuns',[]):
        file=contained(root,previous['path'])
        if previous['path'] in prior_paths or sha(file)!=previous['sha256']:raise ValueError('previous budget evidence differs')
        prior_paths.add(previous['path']);old=read(file)
        prior_metadata+=sum(r.get('metadata',True) for r in old['attempts']);prior_media+=max(old.get('media_bytes',0),old.get('media_reserved',0))
    authorized=authorized_budget(root,site);recorded=data.get('authorizedBudget')
    if not isinstance(recorded,dict) or set(recorded)!=set(authorized) or any(type(n) is not int or n<=0 or n>authorized[k] for k,n in recorded.items()):raise ValueError('real receipt cannot increase ACT-002 authorization')
    if budget['metadata_request_count']+prior_metadata>recorded['metadata_requests'] or max(budget['media_received_bytes'],budget.get('media_reserved_bytes',0))+prior_media>recorded['media_bytes'] or budget['stopped']:raise ValueError('real run exceeded or stopped at its limits')
    if any(r.get('status') not in range(200,400) for r in budget['requests']):raise ValueError('real run ignored request failure')
