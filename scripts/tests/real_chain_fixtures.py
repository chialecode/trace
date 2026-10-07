"""Synthetic validator data only. Never a real-site execution receipt."""
import hashlib,json,shutil,sqlite3
from contextlib import closing
from stage_evidence import sha

def real_fixture(root,source,package):
    authorized={'metadata_requests':30,'media_bytes':512*1024*1024}
    (root/'USER-ACTIONS.md').write_text('<!-- ACT-002-AUTHORIZED-BUDGETS '+json.dumps({s:authorized for s in ['bilibili','douyin']})+' -->\n',encoding='utf8')
    path=root/'synthetic-catalog.db'
    with closing(sqlite3.connect(path)) as db, db:
        db.executescript('''CREATE TABLE source_requests(request_id,platform,kind,entry_id,authentication,credential_generation,snapshot,selected,scope_id,credential_id,validated_at,session_id);
CREATE TABLE subscriptions(subscription_id,acquisition_kind);
CREATE TABLE jobs(job_id,job_type,state,subject_id,payload);
CREATE TABLE media_items(media_id,revision_id,required);
CREATE TABLE current_entry_revisions(revision_id,entry_id);
CREATE TABLE media_variants(variant_id,media_id,blob_id,approval_state);
CREATE TABLE asset_selections(media_id,variant_id);
CREATE TABLE blobs(blob_id,size);
CREATE TABLE resource_acquisitions(variant_id,context_id,outcome);
CREATE TABLE acquisition_contexts(context_id,auth_state,credential_generation,credential_id,validated_at,job_id,job_generation);
CREATE TABLE materializations(subject_id,blob_id,job_id,stage,generation);
CREATE TABLE live_sessions(session_id,state,manual_stopped,ended_at);
CREATE TABLE live_fragments(session_id,media_id,state,start_ms,end_ms);''')
        cases=[]
        for site in ['bilibili','douyin']:
            for kind in ['video','live']:
                rid=f'{site}-{kind}';media=f'media-{rid}';variant=f'variant-{rid}'
                file=root/(rid+'.bin');file.write_bytes(b'SYNTHETIC VALIDATOR FIXTURE '+rid.encode());digest=sha(file);blob='sha256:'+digest
                probe=root/(rid+'-probe.json');probe.write_text(json.dumps({'streams':[{'codec_type':'video','codec_name':'h264','width':3840 if site=='bilibili' and kind=='video' else 1920,'height':2160 if site=='bilibili' and kind=='video' else 1080},{'codec_type':'audio','codec_name':'aac'}],'format':{'duration':'61'}}))
                log=root/(rid+'-decode.log');log.write_text('Synthetic fixture for validator tests only.\n')
                jobs=[rid+'-source',rid+'-media'];db.execute('INSERT INTO source_requests VALUES(?,?,?,?,?,?,?,?,?,?,?,?)',(rid,site,kind,rid,'authenticated',1,json.dumps({'source_duration_seconds':61}),json.dumps([{'id':'choice'}]),rid,'credential','time',rid if kind=='live' else None));db.execute('INSERT INTO subscriptions VALUES(?,?)',(rid,'one_shot'))
                for job,typ in zip(jobs,['source_acquire','media' if kind=='video' else 'record']):db.execute('INSERT INTO jobs VALUES(?,?,?,?,?)',(job,typ,'succeeded',rid,json.dumps({'source_request_id':rid})))
                db.execute('INSERT INTO media_items VALUES(?,?,1)',(media,rid));db.execute('INSERT INTO current_entry_revisions VALUES(?,?)',(rid,rid));db.execute('INSERT INTO media_variants VALUES(?,?,?,?)',(variant,media,blob,'automatic'));db.execute('INSERT INTO asset_selections VALUES(?,?)',(media,variant));db.execute('INSERT INTO blobs VALUES(?,?)',(blob,file.stat().st_size));db.execute('INSERT INTO resource_acquisitions VALUES(?,?,?)',(variant,rid,'acquired'));db.execute('INSERT INTO acquisition_contexts VALUES(?,?,?,?,?,?,?)',(rid,'authenticated',1,'credential','time',jobs[1],1));db.execute('INSERT INTO materializations VALUES(?,?,?,?,?)',(media,blob,jobs[1],'COMMITTED',1))
                if kind=='live':
                    db.execute('INSERT INTO live_sessions VALUES(?,?,?,?)',(rid,'sealed',1,'ended'));db.execute('INSERT INTO live_fragments VALUES(?,?,?,?,?)',(rid,media,'sealed',0,61000))
                exported=root/(rid+'-export.bin');shutil.copyfile(file,exported)
                cases.append({'requestId':rid,'platform':site,'kind':kind,'selectedIds':['choice'],'taskIds':jobs,'guiSelection':True,'offlineRead':True,'offlineReads':[{'blobId':blob,'offset':0,'bytes':file.stat().st_size,'sha256':hashlib.sha256(file.read_bytes()).hexdigest()}],'reopened':True,'receivedSeconds':61,'guiClosedDuringCapture':True,'lifecycle':{'coreInstanceId':'instance','continuedCoreInstanceId':'instance','captureStartedUnix':1,'guiClosedUnix':2,'guiReopenedUnix':65,'guiStop':True},'sealed':True,'exported':True,'exports':[{'path':exported.name,'sha256':digest,'bytes':file.stat().st_size}],'sourceDurationSeconds':61,'objects':[{'mediaId':media,'variantId':variant,'bytes':file.stat().st_size,'sha256':digest,'path':file.name}],'probe':{'path':probe.name,'sha256':sha(probe),'input':file.name,'inputSha256':digest},'decodes':[{'input':file.name,'inputSha256':digest,'inputBytes':file.stat().st_size,'log':log.name,'logSha256':sha(log),'fullFile':True,'networkInputs':0,'exitCode':0}]})
    data={'origin':'packaged_core_gui','status':'passed','sourceInputs':source,'sourceUnchanged':True,'packageManifestSha256':package['files']['manifest.json'],'database':{'path':path.name,'sha256':sha(path)},'cases':cases,'authorizedBudget':authorized,'networkBudget':{'metadata_request_count':2,'media_received_bytes':100,'stopped':False,'requests':[{'status':200},{'status':200}]}}
    out=root/'real-fixture.json';out.write_text(json.dumps(data));return [{'path':out.name,'sha256':sha(out)}]
