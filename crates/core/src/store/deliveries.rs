//! Download requests: the entries of one linked source or one profile selection, the one-shot
//! scope their jobs run under, and the moment all of their work has settled.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction, params};
use serde_json::{Value, json};

/// Job states that mean work is still on its way. `waiting_auth`, `failed` and `cancelled`
/// are settled: they need the user, not time.
const PENDING: &str =
    "('queued','leased','running','waiting_retry','waiting_storage','waiting_plugin','paused')";

pub struct Delivery {
    pub delivery_id: String,
    pub kind: String,
    pub platform: String,
    pub source_url: String,
    pub destination: String,
    pub state: String,
    pub created_at: String,
    pub finished_at: Option<String>,
}

pub struct Item {
    pub position: i64,
    pub native_key: String,
    pub native_kind: String,
    pub title: String,
    pub entry_id: Option<String>,
    pub state: String,
    pub message: Option<String>,
    pub files: i64,
}

pub struct NewDelivery<'a> {
    pub delivery_id: &'a str,
    pub kind: &'a str,
    pub platform: &'a str,
    pub source_url: &'a str,
    pub request_id: Option<&'a str>,
    pub scope_id: &'a str,
    pub destination: &'a str,
}

pub struct NewItem<'a> {
    pub native_key: &'a str,
    pub native_kind: &'a str,
    pub title: &'a str,
    pub entry_id: &'a str,
    /// Nothing to fetch: the library already holds the entry.
    pub ready: bool,
}

/// The internal scope a request's jobs run under. It carries the credential and the default
/// quality choice and is never scanned.
pub fn create_scope(
    tx: &Transaction<'_>,
    scope: &str,
    account_id: &str,
    credential: Option<&str>,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,credential_id,quality_json,acquisition_kind,auto_download,created_at,updated_at) VALUES(?1,?2,?3,?4,?5,'{}','one_shot',1,?6,?6)",
        params![
            scope,
            account_id,
            format!("source:{scope}"),
            json!({"kind": "one_shot"}).to_string(),
            credential,
            now
        ],
    )?;
    Ok(())
}

pub fn insert(
    tx: &Transaction<'_>,
    d: &NewDelivery<'_>,
    items: &[NewItem<'_>],
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO deliveries(delivery_id,kind,platform,source_url,request_id,scope_id,destination,created_at) VALUES(?1,?2,?3,?4,?5,?6,?7,?8)",
        params![
            d.delivery_id,
            d.kind,
            d.platform,
            d.source_url,
            d.request_id,
            d.scope_id,
            d.destination,
            now
        ],
    )?;
    for (position, item) in items.iter().enumerate() {
        tx.execute(
            "INSERT INTO delivery_items(delivery_id,position,native_key,native_kind,title,entry_id,state) VALUES(?1,?2,?3,?4,?5,?6,?7)",
            params![
                d.delivery_id,
                position as i64,
                item.native_key,
                item.native_kind,
                item.title,
                item.entry_id,
                if item.ready { "ready" } else { "queued" }
            ],
        )?;
    }
    Ok(())
}

const SELECT: &str = "SELECT delivery_id,kind,platform,source_url,destination,state,created_at,finished_at FROM deliveries";

fn read(r: &rusqlite::Row<'_>) -> rusqlite::Result<Delivery> {
    Ok(Delivery {
        delivery_id: r.get(0)?,
        kind: r.get(1)?,
        platform: r.get(2)?,
        source_url: r.get(3)?,
        destination: r.get(4)?,
        state: r.get(5)?,
        created_at: r.get(6)?,
        finished_at: r.get(7)?,
    })
}

pub fn load(c: &Connection, id: &str) -> Result<Option<Delivery>, CoreError> {
    Ok(
        c.query_row(&format!("{SELECT} WHERE delivery_id=?1"), [id], read)
            .optional()?,
    )
}

pub fn recent(c: &Connection, limit: i64) -> Result<Vec<Delivery>, CoreError> {
    let rows = c
        .prepare(&format!(
            "{SELECT} ORDER BY created_at DESC,delivery_id LIMIT ?1"
        ))?
        .query_map([limit], read)?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

pub fn items(c: &Connection, id: &str) -> Result<Vec<Item>, CoreError> {
    let rows = c
        .prepare(
            "SELECT position,native_key,native_kind,title,entry_id,state,message,files FROM delivery_items WHERE delivery_id=?1 ORDER BY position",
        )?
        .query_map([id], |r| {
            Ok(Item {
                position: r.get(0)?,
                native_key: r.get(1)?,
                native_kind: r.get(2)?,
                title: r.get(3)?,
                entry_id: r.get(4)?,
                state: r.get(5)?,
                message: r.get(6)?,
                files: r.get(7)?,
            })
        })?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

pub fn set_item(
    tx: &Transaction<'_>,
    id: &str,
    position: i64,
    state: &str,
    message: Option<&str>,
    files: i64,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE delivery_items SET state=?3,message=?4,files=?5 WHERE delivery_id=?1 AND position=?2",
        params![id, position, state, message, files],
    )?;
    Ok(())
}

pub fn set_done(tx: &Transaction<'_>, id: &str, now: &str) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE deliveries SET state='done',finished_at=?2 WHERE delivery_id=?1 AND state='finishing'",
        params![id, now],
    )?;
    Ok(())
}

/// Work of the request that is still on its way.
pub fn pending_jobs(c: &Connection, id: &str) -> Result<i64, CoreError> {
    Ok(c.query_row(
        &format!("SELECT count(*) FROM jobs WHERE json_extract(payload,'$.delivery_id')=?1 AND state IN {PENDING}"),
        [id],
        |r| r.get(0),
    )?)
}

/// Once nothing is on its way any more, queues the job that writes the copies. It runs again
/// when work that was waiting for a login finishes later.
pub fn settle(tx: &Transaction<'_>, id: &str, now: &str) -> Result<bool, CoreError> {
    let state: Option<String> = tx
        .query_row(
            "SELECT state FROM deliveries WHERE delivery_id=?1",
            [id],
            |r| r.get(0),
        )
        .optional()?;
    if !matches!(state.as_deref(), Some("running" | "done")) || pending_jobs(tx, id)? > 0 {
        return Ok(false);
    }
    tx.execute(
        "UPDATE deliveries SET state='finishing',generation=generation+1,finished_at=NULL WHERE delivery_id=?1",
        [id],
    )?;
    let generation: i64 = tx.query_row(
        "SELECT generation FROM deliveries WHERE delivery_id=?1",
        [id],
        |r| r.get(0),
    )?;
    tx.execute(
        "INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,state,priority,created_at,updated_at) VALUES(?1,?2,'delivery',?3,?4,'queued',1,?5,?5)",
        params![
            format!("job-{}", uuid::Uuid::new_v4()),
            format!("delivery:{id}:{generation}"),
            id,
            json!({"deliver": id}).to_string(),
            now
        ],
    )?;
    Ok(true)
}

/// Called whenever a job reaches a state it will not leave on its own.
pub fn job_settled(tx: &Transaction<'_>, payload: &Value, now: &str) -> Result<(), CoreError> {
    if let Some(id) = payload["delivery_id"].as_str() {
        settle(tx, id, now)?;
    }
    Ok(())
}

/// Stops the request: its work is cancelled and its scope can no longer run anything.
pub fn cancel(tx: &Transaction<'_>, id: &str) -> Result<bool, CoreError> {
    let changed = tx.execute(
        "UPDATE deliveries SET state='cancelled',finished_at=COALESCE(finished_at,?2) WHERE delivery_id=?1 AND state!='cancelled'",
        params![id, crate::jobs::now()],
    )?;
    if changed == 0 {
        return Ok(false);
    }
    tx.execute(
        "UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?2 WHERE (json_extract(payload,'$.delivery_id')=?1 OR (job_type='delivery' AND subject_id=?1)) AND state NOT IN ('succeeded','failed','cancelled')",
        params![id, crate::jobs::now()],
    )?;
    tx.execute(
        "UPDATE delivery_items SET state='cancelled' WHERE delivery_id=?1 AND state IN ('queued')",
        [id],
    )?;
    tx.execute(
        "UPDATE subscriptions SET state='paused',config_version=config_version+1 WHERE subscription_id=(SELECT scope_id FROM deliveries WHERE delivery_id=?1)",
        [id],
    )?;
    Ok(true)
}

/// Why an entry of the request has nothing saved: the class its latest stopped job recorded.
pub fn stopped_class(c: &Connection, id: &str, entry: &str) -> Result<Option<String>, CoreError> {
    let raw: Option<Option<String>> = c
        .query_row(
            "SELECT last_error FROM jobs WHERE json_extract(payload,'$.delivery_id')=?1 AND subject_id=?2 AND state IN ('failed','waiting_auth','cancelled') ORDER BY updated_at DESC LIMIT 1",
            params![id, entry],
            |r| r.get(0),
        )
        .optional()?;
    let Some(Some(raw)) = raw else {
        return Ok(None);
    };
    Ok(serde_json::from_str::<Value>(&raw)
        .ok()
        .and_then(|v| v["class"].as_str().map(str::to_owned)))
}

/// Whether the request's work was stopped by something other than the user.
pub fn any_stopped(c: &Connection, id: &str, entry: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM jobs WHERE json_extract(payload,'$.delivery_id')=?1 AND subject_id=?2 AND state IN ('failed','waiting_auth'))",
        params![id, entry],
        |r| r.get(0),
    )?)
}

/// A linked entry that is to be copied to a folder when it is saved. Returns the request's id.
pub fn attach_link(
    tx: &Transaction<'_>,
    scope: &str,
    request_id: &str,
    snapshot: &Value,
    entry: &str,
    target: &trace_contracts::preview::DownloadTarget,
) -> Result<String, CoreError> {
    let (platform, source_url): (String, String) = tx.query_row(
        "SELECT platform,source_url FROM source_requests WHERE request_id=?1",
        [request_id],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?;
    let id = format!("delivery-{}", uuid::Uuid::new_v4());
    insert(
        tx,
        &NewDelivery {
            delivery_id: &id,
            kind: "link",
            platform: &platform,
            source_url: &source_url,
            request_id: Some(request_id),
            scope_id: scope,
            destination: &serde_json::to_string(target)?,
        },
        &[NewItem {
            native_key: snapshot["native_key"].as_str().unwrap_or(""),
            native_kind: snapshot["native_kind"].as_str().unwrap_or("video"),
            title: snapshot["title"].as_str().unwrap_or(""),
            entry_id: entry,
            ready: false,
        }],
        &crate::jobs::now(),
    )?;
    Ok(id)
}

/// Cancelling a source request stops the download request that came with it.
pub fn cancel_for_request(tx: &Transaction<'_>, request_id: &str) -> Result<(), CoreError> {
    let ids = tx
        .prepare("SELECT delivery_id FROM deliveries WHERE request_id=?1")?
        .query_map([request_id], |r| r.get::<_, String>(0))?
        .collect::<Result<Vec<_>, _>>()?;
    for id in ids {
        cancel(tx, &id)?;
    }
    Ok(())
}
