//! Optional finite live-chat history samples. They never imply full coverage,
//! never gate media completeness, and use the same subscription lease fence.
use crate::{
    core::Core,
    error::CoreError,
    jobs::{self, ClaimedJob},
};
use serde_json::{Value, json};
use trace_contracts::plugin::LiveAuxiliary;

pub async fn schedule(core: &Core, guard: &ClaimedJob, session: &str) -> Result<(), CoreError> {
    let guard = guard.clone();
    let session = session.to_owned();
    core.db.write(move|tx| {
        guard.check(tx)?;
        tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,next_run_at,created_at,updated_at) SELECT 'aux:'||l.session_id,'aux:'||l.session_id,'live_auxiliary',l.session_id,json_object('subscription_id',s.subscription_id),3,'queued',?2,?2,?2 FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id WHERE l.session_id=?1 AND s.capture_chat=1 AND l.manual_stopped=0 AND l.ended_at IS NULL ON CONFLICT(job_key) DO UPDATE SET state='queued',next_run_at=excluded.next_run_at WHERE jobs.state='succeeded'",rusqlite::params![session,jobs::now()])?;
        Ok(())
    }).await
}

pub async fn run(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let guard = job.clone();
    let (namespace,room,enabled):(String,String,bool)=core.db.read(move|c| {
        guard.check(c)?;
        Ok(c.query_row("SELECT a.namespace,l.room_key,s.capture_chat=1 AND l.manual_stopped=0 AND l.ended_at IS NULL AND l.state NOT IN ('stopping','sealed') FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id JOIN source_accounts a ON a.account_id=s.account_id WHERE l.session_id=?1",[guard.subject_id],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?)))?)
    }).await?;
    if !enabled {
        return Ok(());
    }
    core.metadata_pacing(&namespace).await;
    let result = core
        .adapter_call(
            job,
            &namespace,
            "sample_live_auxiliary",
            json!({"room_key":room}),
            Some(30_000),
        )
        .await
        .and_then(|v| serde_json::from_value::<LiveAuxiliary>(v).map_err(Into::into));
    let result = match result {
        Ok(page) => persist(core, job, page).await,
        Err(error) => Err(error),
    };
    match result {
        Ok(()) => Ok(()),
        Err(error) => {
            let guard = job.clone();
            let failure = format!("{:?}", error.failure_class());
            core.db.write(move|tx| {
                guard.check(tx)?;
                tx.execute("INSERT INTO live_auxiliary_samples(session_id,observed_at,coverage,failure_class) VALUES(?1,?2,'failed',?3)",rusqlite::params![guard.subject_id,jobs::now(),failure])?;
                Ok(())
            }).await?;
            Err(error)
        }
    }
}

fn text(value: &str, max: usize) -> String {
    trace_contracts::content::safe_metadata(&json!(value))
        .as_str()
        .unwrap_or("[omitted]")
        .chars()
        .take(max)
        .collect()
}
async fn persist(core: &Core, guard: &ClaimedJob, page: LiveAuxiliary) -> Result<(), CoreError> {
    if page.coverage != "sampled_history"
        || page.messages.len() > 100
        || page.source_count > 100
        || page.omitted as usize + page.messages.len() != page.source_count as usize
        || serde_json::to_vec(&page)?.len() > 128 * 1024
    {
        return Err(CoreError::InvalidInput(
            "live auxiliary sample limits".into(),
        ));
    }
    let guard = guard.clone();
    core.db.write(move|tx| {
        guard.check(tx)?;
        let enabled:bool=tx.query_row("SELECT s.capture_chat=1 AND l.manual_stopped=0 AND l.ended_at IS NULL FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id WHERE l.session_id=?1",[&guard.subject_id],|r|r.get(0))?;
        if !enabled {return Err(CoreError::Cancelled)}
        let ts=jobs::now();let mut saved=0;
        for msg in page.messages {
            if msg.source_id.is_empty() || msg.source_id.len()>128 || !msg.source_id.bytes().all(|b|b.is_ascii_alphanumeric()||b==b'-'||b==b'_') || msg.kind!="chat" {return Err(CoreError::InvalidInput("live message identity".into()))}
            saved+=tx.execute("INSERT OR IGNORE INTO live_messages(session_id,source_id,source_time,author,body,kind,observed_at) VALUES(?1,?2,?3,?4,?5,'chat',?6)",rusqlite::params![guard.subject_id,msg.source_id,msg.source_time.map(|v|text(&v,64)),msg.author.map(|v|text(&v,256)),text(&msg.text,4096),ts])?;
        }
        tx.execute("INSERT INTO live_auxiliary_samples(session_id,observed_at,coverage,source_count,saved_count,omitted) VALUES(?1,?2,'sampled_history',?3,?4,?5)",rusqlite::params![guard.subject_id,ts,page.source_count,saved as i64,page.omitted])?;
        let next=(time::OffsetDateTime::now_utc()+time::Duration::seconds(10)).format(&time::format_description::well_known::Rfc3339).map_err(|_|CoreError::Internal("clock".into()))?;
        tx.execute("UPDATE jobs SET state='queued',lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2 WHERE job_id=?1",rusqlite::params![guard.job_id,next])?;
        crate::objects::outbox_emit(tx,"live.updated",&guard.subject_id,&json!({"auxiliary_sample":true}))?;
        Ok(())
    }).await
}

pub async fn page(core: &Core, session: String, before: Option<i64>) -> Result<Value, CoreError> {
    core.db.read(move|c| {
        let mut items=c.prepare("SELECT sequence,source_id,source_time,author,body,observed_at FROM live_messages WHERE session_id=?1 AND sequence<?2 ORDER BY sequence DESC LIMIT 31")?.query_map(rusqlite::params![session,before.unwrap_or(i64::MAX)],|r|Ok(json!({"sequence":r.get::<_,i64>(0)?,"source_id":r.get::<_,String>(1)?,"source_time":r.get::<_,Option<String>>(2)?,"author":r.get::<_,Option<String>>(3)?,"text":r.get::<_,String>(4)?,"observed_at":r.get::<_,String>(5)?})))?.collect::<Result<Vec<_>,_>>()?;
        let more=items.len()>30;items.truncate(30);
        let cursor=if more {items.last().map(|r|r["sequence"].clone())} else {None};
        let total:i64=c.query_row("SELECT count(*) FROM live_messages WHERE session_id=?1",[&session],|r|r.get(0))?;
        use rusqlite::OptionalExtension;
        let latest:Option<Value>=c.query_row("SELECT observed_at,coverage,source_count,saved_count,omitted,failure_class FROM live_auxiliary_samples WHERE session_id=?1 ORDER BY sequence DESC LIMIT 1",[&session],|r|Ok(json!({"observed_at":r.get::<_,String>(0)?,"coverage":r.get::<_,String>(1)?,"source_count":r.get::<_,i64>(2)?,"saved_count":r.get::<_,i64>(3)?,"omitted":r.get::<_,i64>(4)?,"failure_class":r.get::<_,Option<String>>(5)?}))).optional()?;
        let enabled:bool=c.query_row("SELECT s.capture_chat FROM subscriptions s JOIN live_sessions l ON l.subscription_id=s.subscription_id WHERE l.session_id=?1",[session],|r|r.get(0))?;
        Ok(json!({"items":items,"next_before":cursor,"total":total,"latest":latest,"enabled":enabled,"coverage":"sampled_history"}))
    }).await
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn c04_c06_live_auxiliary_dedup_budget_pause_and_secrets() {
        let (_dir, core, scan) = super::super::tests::fixture().await;
        let sid = super::super::register(&core, &scan, super::super::tests::snapshot())
            .await
            .unwrap()
            .unwrap();
        schedule(&core, &scan, &sid).await.unwrap();
        let n: i64 = core
            .db
            .read(|c| {
                Ok(c.query_row(
                    "SELECT count(*) FROM jobs WHERE job_type='live_auxiliary'",
                    [],
                    |r| r.get(0),
                )?)
            })
            .await
            .unwrap();
        assert_eq!(n, 0, "user selection is required");
        core.db
            .write(|tx| {
                tx.execute("UPDATE subscriptions SET capture_chat=1", [])?;
                tx.execute(
                    "UPDATE jobs SET state='succeeded' WHERE job_type='record'",
                    [],
                )?;
                Ok(())
            })
            .await
            .unwrap();
        schedule(&core, &scan, &sid).await.unwrap();
        let mut job = jobs::claim_due(&core.db, "test", 1)
            .await
            .unwrap()
            .remove(0);
        let sample = LiveAuxiliary {
            messages: vec![trace_contracts::plugin::LiveMessage {
                source_id: "90071992547409931".into(),
                author: Some("公开署名".into()),
                text: "安全文本".into(),
                source_time: Some("123".into()),
                kind: "chat".into(),
            }],
            coverage: "sampled_history".into(),
            source_count: 1,
            omitted: 0,
        };
        for _ in 0..2 {
            job.mark_running(&core.db).await.unwrap();
            persist(&core, &job, sample.clone()).await.unwrap();
            core.db
                .write(|tx| {
                    tx.execute(
                        "UPDATE jobs SET next_run_at=NULL WHERE job_type='live_auxiliary'",
                        [],
                    )?;
                    Ok(())
                })
                .await
                .unwrap();
            job = jobs::claim_due(&core.db, "test", 1)
                .await
                .unwrap()
                .remove(0);
        }
        let view = page(&core, sid.clone(), None).await.unwrap();
        assert_eq!(view["total"], 1);
        assert_eq!(view["latest"]["saved_count"], 0);
        assert_eq!(view["items"][0]["source_id"], "90071992547409931");
        let mut bad = sample.clone();
        bad.coverage = "complete".into();
        assert!(persist(&core, &job, bad).await.is_err());
        core.db
            .write(|tx| jobs::set_subscription_enabled(tx, "s", false))
            .await
            .unwrap();
        assert!(persist(&core, &job, sample).await.is_err());
        assert_eq!(page(&core, sid, None).await.unwrap()["total"], 1);
        assert!(
            !text("https://media.invalid/a?a_bogus=PRIVATE_SENTINEL", 4096)
                .contains("PRIVATE_SENTINEL")
        );
        core.shutdown().await;
    }
}
