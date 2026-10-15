use super::schema::{configure, migrations, run_migrations};
use rusqlite::Connection;
use tempfile::TempDir;

const SEED: &str = "
INSERT INTO creators(creator_id,display_name,created_at,updated_at) VALUES('c1','甲','t','t');
INSERT INTO creators(creator_id,display_name,created_at,updated_at) VALUES('c2','乙','t','t');
INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t');
INSERT INTO account_bindings(binding_id,creator_id,account_id,state,created_at) VALUES('b','c1','a','active','t');
INSERT INTO account_observations(account_id,display_name,observed_at,evidence) VALUES('a','甲','t','{}');
INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','weibo','post','1','a','t');";

/// A database as schema 7 left it, with creators, one bound account, an observation and an entry.
fn schema_seven(path: &std::path::Path) -> Connection {
    let conn = Connection::open(path).unwrap();
    configure(&conn).unwrap();
    for (version, sql) in migrations().into_iter().filter(|(v, _)| *v <= 7) {
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
fn schema_seven_databases_gain_the_management_tables_without_rewriting_content() {
    let dir = TempDir::new().unwrap();
    let path = dir.path().join("catalog.sqlite3");
    let mut conn = schema_seven(&path);
    run_migrations(&mut conn).unwrap();
    assert_eq!(version(&conn), 8);
    // The observation from schema 7 survives with an empty profile record.
    let profile: String = conn
        .query_row(
            "SELECT profile_json FROM account_observations WHERE account_id='a'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(profile, "{}");
    // The entry and its binding are untouched by the migration.
    let entry: String = conn
        .query_row(
            "SELECT account_id FROM entries WHERE entry_id='e'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(entry, "a");
    let tables: i64 = conn
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('creator_redirects','creator_operations','identity_candidates','live_replay_links','import_batches','import_items','resource_signatures','resource_comparisons','work_groups','work_group_members','plugin_releases','management_operations')",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(tables, 12);
    let backups = std::fs::read_dir(dir.path())
        .unwrap()
        .filter_map(Result::ok)
        .filter(|e| e.file_name().to_string_lossy().contains("pre-schema-8"))
        .count();
    assert_eq!(backups, 1, "a snapshot is kept before migrating");
}

#[test]
fn an_interrupted_schema_eight_migration_leaves_schema_seven_usable_and_resumable() {
    let dir = TempDir::new().unwrap();
    let path = dir.path().join("catalog.sqlite3");
    let mut conn = schema_seven(&path);
    conn.execute_batch("CREATE TABLE creator_redirects(x INTEGER);")
        .unwrap();
    assert!(run_migrations(&mut conn).is_err());
    assert_eq!(version(&conn), 7);
    let columns: i64 = conn
        .query_row(
            "SELECT count(*) FROM pragma_table_info('account_observations') WHERE name='profile_json'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(columns, 0, "nothing of the failed migration remains");
    conn.execute_batch("DROP TABLE creator_redirects;").unwrap();
    run_migrations(&mut conn).unwrap();
    assert_eq!(version(&conn), 8);
}

#[test]
fn schema_eight_enforces_its_invariants_and_older_programs_refuse_it() {
    let mut conn = Connection::open_in_memory().unwrap();
    configure(&conn).unwrap();
    run_migrations(&mut conn).unwrap();
    conn.execute_batch(SEED).unwrap();
    assert!(
        conn.execute(
            "INSERT INTO creator_redirects(source_creator_id,target_creator_id,created_at) VALUES('c1','c1','t')",
            [],
        )
        .is_err(),
        "a creator cannot merge into itself"
    );
    conn.execute(
        "INSERT INTO creator_redirects(source_creator_id,target_creator_id,created_at) VALUES('c1','c2','t')",
        [],
    )
    .unwrap();
    let candidate = "INSERT INTO identity_candidates(candidate_id,account_id,proposed_creator_id,kind,score,created_at) VALUES(?1,'a','c2','similar_name',0.9,'t')";
    conn.execute(candidate, ["k1"]).unwrap();
    assert!(
        conn.execute(candidate, ["k2"]).is_err(),
        "one candidate per account, creator and kind"
    );
    let item = "INSERT INTO import_items(item_id,batch_id,position,source_path,rel_path,created_at,updated_at) VALUES(?1,'batch',0,'C:/x','a.txt','t','t')";
    conn.execute(
        "INSERT INTO import_batches(batch_id,mode,source_path,created_at,updated_at) VALUES('batch','in_place','C:/x','t','t')",
        [],
    )
    .unwrap();
    conn.execute(item, ["i1"]).unwrap();
    assert!(
        conn.execute(item, ["i2"]).is_err(),
        "a batch records each relative path once"
    );
    conn.execute(
        "INSERT INTO plugin_releases(plugin_id,version,manifest_json,path,digest,protocol,installed_at) VALUES('p','1.0','{}','p/1.0','d','0.1','t')",
        [],
    )
    .unwrap();
    assert!(
        conn.execute(
            "INSERT INTO plugin_releases(plugin_id,version,manifest_json,path,digest,protocol,installed_at) VALUES('p','1.0','{}','p/1.0','d','0.1','t')",
            [],
        )
        .is_err(),
        "a release version is installed once"
    );
    conn.execute("UPDATE meta SET value='9' WHERE key='schema_version'", [])
        .unwrap();
    assert!(run_migrations(&mut conn).is_err());
}
