use super::schema::{configure, migrations, run_migrations};
use rusqlite::Connection;
use tempfile::TempDir;

const SEED: &str = "
INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','2026-01-01T00:00:00Z');
INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','video','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','bilibili','video','BV1','a','2026-01-01T00:00:00Z');
INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','t','b','2026-01-01T00:00:00Z');
INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES('m','r','video',0,'v');
INSERT INTO source_requests(request_id,platform,kind,source_url,native_key,account_key,authentication,snapshot,created_at,expires_at) VALUES('q','bilibili','video','https://www.bilibili.com/video/BV1','BV1','1','anonymous','{}','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z');";

const REQUEST: &str = "INSERT INTO source_requests(request_id,platform,kind,source_url,native_key,account_key,authentication,snapshot,created_at,expires_at) VALUES(?1,'x',?2,'https://x.com/a/status/1','1','a','anonymous','{}','t','t')";

/// A database as schema 6 left it, with one account, subscription, entry and media row.
fn schema_six(path: &std::path::Path) -> Connection {
    let conn = Connection::open(path).unwrap();
    configure(&conn).unwrap();
    for (version, sql) in migrations().into_iter().filter(|(v, _)| *v <= 6) {
        conn.execute_batch(sql).unwrap();
        conn.execute(
            "INSERT INTO meta(key,value) VALUES('schema_version',?1) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
            [version.to_string()],
        )
        .unwrap();
    }
    conn.execute_batch(SEED).unwrap();
    conn
}

fn version(conn: &Connection) -> i64 {
    conn.query_row(
        "SELECT value FROM meta WHERE key='schema_version'",
        [],
        |r| r.get::<_, String>(0),
    )
    .unwrap()
    .parse()
    .unwrap()
}

#[test]
fn existing_subscriptions_keep_downloading_and_old_rows_survive() {
    let dir = TempDir::new().unwrap();
    let path = dir.path().join("catalog.sqlite3");
    let mut conn = schema_six(&path);
    run_migrations(&mut conn).unwrap();
    assert_eq!(version(&conn), 8);
    let auto: i64 = conn
        .query_row(
            "SELECT auto_download FROM subscriptions WHERE subscription_id='s'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(
        auto, 1,
        "an existing subscription must keep downloading videos"
    );
    let fetch: String = conn
        .query_row(
            "SELECT fetch_state FROM media_items WHERE media_id='m'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(fetch, "wanted");
    let kind: String = conn
        .query_row(
            "SELECT kind FROM source_requests WHERE request_id='q'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(kind, "video");
    conn.execute(REQUEST, rusqlite::params!["q2", "entry"])
        .expect("a linked entry of any kind may now be requested");
    assert!(
        conn.execute(REQUEST, rusqlite::params!["q3", "bogus"])
            .is_err()
    );
    let backups = std::fs::read_dir(dir.path())
        .unwrap()
        .filter_map(Result::ok)
        .filter(|e| e.file_name().to_string_lossy().contains("pre-schema-7"))
        .count();
    assert_eq!(
        backups, 1,
        "a snapshot of the schema-6 database is kept before migrating"
    );
}

#[test]
fn an_interrupted_migration_leaves_the_old_database_intact_and_resumable() {
    let dir = TempDir::new().unwrap();
    let path = dir.path().join("catalog.sqlite3");
    let mut conn = schema_six(&path);
    // A leftover table makes the migration fail halfway through its script.
    conn.execute_batch("CREATE TABLE network_settings(x INTEGER);")
        .unwrap();
    assert!(run_migrations(&mut conn).is_err());
    assert_eq!(version(&conn), 6);
    let leftovers: i64 = conn
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE name IN ('entry_meta','site_rules','source_requests_v7','app_settings')",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(leftovers, 0, "nothing of the failed migration remains");
    let columns: i64 = conn
        .query_row(
            "SELECT count(*) FROM pragma_table_info('subscriptions') WHERE name='auto_download'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(columns, 0);
    conn.execute_batch("DROP TABLE network_settings;").unwrap();
    run_migrations(&mut conn).unwrap();
    assert_eq!(version(&conn), 8);
}

#[test]
fn schema_seven_enforces_its_invariants_and_older_programs_refuse_it() {
    let mut conn = Connection::open_in_memory().unwrap();
    configure(&conn).unwrap();
    run_migrations(&mut conn).unwrap();
    let location = "INSERT INTO storage_locations(location_id,kind,path,is_default,created_at) VALUES(?1,'download',?2,1,'t')";
    conn.execute(location, ["d1", "C:/a"]).unwrap();
    assert!(
        conn.execute(location, ["d2", "C:/b"]).is_err(),
        "only one default download location"
    );
    let migration = "INSERT INTO library_migrations(migration_id,from_path,to_path,state,created_at,updated_at) VALUES(?1,'a',?2,'copying','t','t')";
    conn.execute(migration, ["m1", "b"]).unwrap();
    assert!(
        conn.execute(migration, ["m2", "c"]).is_err(),
        "only one library migration may be open"
    );
    conn.execute("UPDATE meta SET value='9' WHERE key='schema_version'", [])
        .unwrap();
    assert!(run_migrations(&mut conn).is_err());
}
