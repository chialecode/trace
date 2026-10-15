//! daemon 命令名常量（客户端与服务端共同引用，避免拼写漂移）。

pub const DAEMON_STATUS: &str = "daemon.status";
pub const DAEMON_PAUSE: &str = "daemon.pause";
pub const DAEMON_SHUTDOWN: &str = "daemon.shutdown";
pub const CREATOR_CREATE: &str = "creator.create";
pub const CREATOR_UPDATE: &str = "creator.update";
pub const CREATOR_LIST: &str = "creator.list";
pub const CREATOR_GET: &str = "creator.get";
pub const ACCOUNT_BIND: &str = "account.bind";
pub const ACCOUNT_UNBIND: &str = "account.unbind";
pub const ACCOUNT_UPDATE: &str = "account.update";
pub const ACCOUNT_LIST: &str = "account.list";
pub const SUBSCRIPTION_CREATE: &str = "subscription.create";
pub const SUBSCRIPTION_UPDATE: &str = "subscription.update";
pub const SUBSCRIPTION_CHECK_NOW: &str = "subscription.check_now";
pub const SUBSCRIPTION_LIST: &str = "subscription.list";
pub const ARCHIVE_TIMELINE: &str = "archive.timeline";
pub const ARCHIVE_ENTRY_GET: &str = "archive.entry.get";
/// Read-only, credential-free evidence for one subscription's incremental check.
pub const ARCHIVE_SUBSCRIPTION_EVIDENCE: &str = "archive.subscription.evidence";
pub const JOB_LIST: &str = "job.list";
pub const JOB_GET: &str = "job.get";
pub const JOB_RETRY: &str = "job.retry";
pub const JOB_CANCEL: &str = "job.cancel";
pub const CREDENTIAL_LIST: &str = "credential.list";
pub const CREDENTIAL_IMPORT: &str = "credential.import";
pub const CREDENTIAL_VALIDATE: &str = "credential.validate";
pub const CREDENTIAL_REVOKE: &str = "credential.revoke";
pub const PLUGIN_LIST: &str = "plugin.list";
pub const PLUGIN_SET_STATE: &str = "plugin.set_state";
pub const STORAGE_USAGE: &str = "storage.usage";
pub const EXPORT_START: &str = "storage.export.start";
pub const BACKUP_START: &str = "storage.backup.start";
pub const BACKUP_LIST: &str = "storage.backup.list";
pub const RESTORE_START: &str = "storage.restore.start";
pub const ARCHIVE_OPEN_SOURCE: &str = "archive.open_source";
pub const MEDIA_RELEASE: &str = "media.release";
pub const MEDIA_OPEN_EXTERNAL: &str = "media.open_external";
pub const MEDIA_LEASE: &str = "media.lease";
pub const MEDIA_READ: &str = "media.read_range";
pub const SEARCH_QUERY: &str = "search.query";
pub const SITE_CATALOG: &str = "site.catalog";
pub const SITE_RECOGNIZE: &str = "site.recognize";
pub const SITE_REFRESH: &str = "site.refresh";
pub const LIBRARY_STATUS: &str = "library.status";
pub const LIBRARY_MIGRATE: &str = "library.migrate";
pub const LIBRARY_SWITCH: &str = "library.switch";
pub const LIBRARY_CANCEL: &str = "library.cancel";
pub const LIBRARY_RELEASE_OLD: &str = "library.release_old";
pub const NETWORK_GET: &str = "network.get";
pub const NETWORK_SET: &str = "network.set";
pub const ARCHIVE_ENTRY_SYNC: &str = "archive.entry.sync";
pub const ARCHIVE_RECHECK: &str = "archive.recheck";
pub const SOURCE_PROFILE_PREVIEW: &str = "source.profile.preview";
pub const SOURCE_PROFILE_DOWNLOAD: &str = "source.profile.download";
pub const SOURCE_DELIVERIES: &str = "source.deliveries";
pub const SOURCE_DELIVERY_CANCEL: &str = "source.delivery.cancel";
pub const DOWNLOAD_SETTINGS: &str = "download.settings";
pub const DOWNLOAD_SETTINGS_SET: &str = "download.settings_set";
pub const DOWNLOAD_EXPORT: &str = "download.export";
pub const DOWNLOAD_CHECK: &str = "download.check";
pub const DOWNLOAD_COPIES: &str = "download.copies";
pub const DOWNLOAD_SYNC: &str = "download.sync";
pub const DOWNLOAD_FORGET: &str = "download.forget";
pub const EXPLORER_TREE: &str = "explorer.tree";
pub const EXPLORER_LIST: &str = "explorer.list";
pub const EXPLORER_OPEN: &str = "explorer.open";
pub const EXPLORER_REVEAL: &str = "explorer.reveal";
pub const ARCHIVE_TAGS: &str = "archive.tags";
pub const ACCOUNT_RESOLVE: &str = "account.resolve";
pub const ACCOUNT_HISTORY: &str = "account.history";
pub const ARCHIVE_ENTRY_PAGE: &str = "archive.entry.page";
pub const ARCHIVE_REVISION_READ: &str = "archive.revision.read";
pub const ARCHIVE_OBSERVATION_READ: &str = "archive.observation.read";
pub const ARCHIVE_ENTRY_RECHECK: &str = "archive.entry.recheck";
pub const ARCHIVE_ACQUISITIONS: &str = "archive.acquisitions";
pub const ARCHIVE_DELETE_PREVIEW: &str = "archive.delete_preview";
pub const ARCHIVE_DELETE: &str = "archive.delete";
pub const ARCHIVE_DELETED: &str = "archive.deleted";
pub const ARCHIVE_RESTORE_DELETED: &str = "archive.restore_deleted";
pub const ARCHIVE_RESOURCES: &str = "archive.resources";
pub const JOB_RETRY_FAILED: &str = "job.retry_failed";
pub const CREDENTIAL_UPDATE: &str = "credential.update";
pub const LIVE_SET_RECORDING: &str = "live.set_recording";
pub const LIVE_FRAGMENTS: &str = "live.fragments";
pub const LIVE_AUXILIARY: &str = "live.auxiliary";
pub const QUALITY_LIST: &str = "quality.list";
pub const QUALITY_CONFIGURE: &str = "quality.configure";
pub const QUALITY_PIN: &str = "quality.pin";
pub const QUALITY_RECYCLE: &str = "quality.recycle";
pub const HEALTH_LIST: &str = "health.list";
pub const HEALTH_REFS: &str = "health.refs";
pub const CACHE_STATUS: &str = "cache.status";
pub const CACHE_CONFIGURE: &str = "cache.configure";
pub const CACHE_CONTROL: &str = "cache.control";
pub const UPGRADE_LIST: &str = "upgrade.list";
pub const UPGRADE_RESOURCES: &str = "upgrade.resources";
pub const UPGRADE_DECIDE: &str = "upgrade.decide";
pub const SOURCE_PREVIEW: &str = "source.preview";
pub const SOURCE_CONFIRM: &str = "source.confirm";
pub const SOURCE_LIST: &str = "source.list";
pub const SOURCE_CANCEL: &str = "source.cancel";
// Management capabilities (stage plan §11) and the manual tool update (DEC-039).
pub const CREATOR_MERGE: &str = "creator.merge";
pub const CREATOR_MERGE_PREVIEW: &str = "creator.merge_preview";
pub const CREATOR_SPLIT: &str = "creator.split";
pub const CREATOR_SPLIT_PREVIEW: &str = "creator.split_preview";
pub const CREATOR_RELINK: &str = "creator.relink";
pub const CREATOR_RELINK_PREVIEW: &str = "creator.relink_preview";
pub const CREATOR_OPERATIONS: &str = "creator.operations";
pub const IDENTITY_CANDIDATES: &str = "identity.candidates";
pub const IDENTITY_CONFIRM: &str = "identity.confirm";
pub const IDENTITY_REJECT: &str = "identity.reject";
pub const ACCOUNT_PROFILE_HISTORY: &str = "account.profile_history";
pub const LIVE_REPLAY_CANDIDATES: &str = "live.replay.candidates";
pub const LIVE_REPLAY_LINK: &str = "live.replay.link";
pub const LIVE_REPLAY_REJECT: &str = "live.replay.reject";
pub const LIVE_REPLAY_LINKS: &str = "live.replay.links";
pub const IMPORT_INSPECT: &str = "import.inspect";
pub const IMPORT_START: &str = "import.start";
pub const IMPORT_STATUS: &str = "import.status";
pub const IMPORT_CANCEL: &str = "import.cancel";
pub const IMPORT_RESUME: &str = "import.resume";
pub const IMPORT_ASSIGN: &str = "import.assign";
pub const IMPORT_CHECK: &str = "import.check";
pub const RESOURCE_DUPLICATES: &str = "resource.duplicates";
pub const RESOURCE_SIMILAR: &str = "resource.similar";
pub const RESOURCE_COMPARE: &str = "resource.compare";
pub const RESOURCE_COMPARE_DECIDE: &str = "resource.compare.decide";
pub const WORK_GROUP_CREATE: &str = "work.group.create";
pub const WORK_GROUP_UPDATE: &str = "work.group.update";
pub const WORK_GROUP_DELETE: &str = "work.group.delete";
pub const WORK_GROUP_ADD: &str = "work.group.add";
pub const WORK_GROUP_REMOVE: &str = "work.group.remove";
pub const WORK_GROUP_LIST: &str = "work.group.list";
pub const WORK_GROUP_GET: &str = "work.group.get";
pub const WORK_COMPARE: &str = "work.compare";
pub const WORK_BATCH_PREVIEW: &str = "work.batch.preview";
pub const WORK_BATCH_APPLY: &str = "work.batch.apply";
pub const PLUGIN_INSTALL: &str = "plugin.install";
pub const PLUGIN_ACTIVATE: &str = "plugin.activate";
pub const PLUGIN_UPGRADE: &str = "plugin.upgrade";
pub const PLUGIN_ROLLBACK: &str = "plugin.rollback";
pub const PLUGIN_REMOVE: &str = "plugin.remove";
pub const PLUGIN_RELEASES: &str = "plugin.releases";
pub const PLUGIN_CAPABILITIES: &str = "plugin.capabilities";
pub const PLUGIN_CAPABILITY_SET: &str = "plugin.capability_set";
pub const TOOL_STATUS: &str = "tool.status";
pub const TOOL_CHECK_UPDATE: &str = "tool.check_update";
pub const TOOL_APPLY_UPDATE: &str = "tool.apply_update";
pub const TOOL_ROLLBACK: &str = "tool.rollback";
pub const MANAGEMENT_OPERATIONS: &str = "management.operations";

/// Commands the desktop UI may forward through the generic `daemon_call`. Daemon
/// shutdown, creator/account removal and media leases stay out: the desktop process
/// issues those itself.
pub const UI_COMMANDS: &[&str] = &[
    DAEMON_STATUS,
    DAEMON_PAUSE,
    CREATOR_CREATE,
    CREATOR_UPDATE,
    CREATOR_LIST,
    ACCOUNT_BIND,
    ACCOUNT_LIST,
    ACCOUNT_RESOLVE,
    ACCOUNT_HISTORY,
    SUBSCRIPTION_CREATE,
    SUBSCRIPTION_UPDATE,
    SUBSCRIPTION_CHECK_NOW,
    SUBSCRIPTION_LIST,
    ARCHIVE_TIMELINE,
    ARCHIVE_ENTRY_GET,
    ARCHIVE_ENTRY_PAGE,
    ARCHIVE_REVISION_READ,
    ARCHIVE_OBSERVATION_READ,
    ARCHIVE_ENTRY_RECHECK,
    ARCHIVE_ACQUISITIONS,
    ARCHIVE_DELETE_PREVIEW,
    ARCHIVE_DELETE,
    ARCHIVE_DELETED,
    ARCHIVE_RESTORE_DELETED,
    ARCHIVE_RESOURCES,
    ARCHIVE_OPEN_SOURCE,
    JOB_LIST,
    JOB_GET,
    JOB_RETRY,
    JOB_RETRY_FAILED,
    JOB_CANCEL,
    CREDENTIAL_LIST,
    CREDENTIAL_IMPORT,
    CREDENTIAL_UPDATE,
    CREDENTIAL_VALIDATE,
    CREDENTIAL_REVOKE,
    PLUGIN_LIST,
    PLUGIN_SET_STATE,
    STORAGE_USAGE,
    EXPORT_START,
    BACKUP_START,
    BACKUP_LIST,
    RESTORE_START,
    MEDIA_OPEN_EXTERNAL,
    SEARCH_QUERY,
    LIVE_SET_RECORDING,
    LIVE_FRAGMENTS,
    LIVE_AUXILIARY,
    QUALITY_LIST,
    QUALITY_CONFIGURE,
    QUALITY_PIN,
    QUALITY_RECYCLE,
    HEALTH_LIST,
    HEALTH_REFS,
    CACHE_STATUS,
    CACHE_CONFIGURE,
    CACHE_CONTROL,
    UPGRADE_LIST,
    UPGRADE_RESOURCES,
    UPGRADE_DECIDE,
    SOURCE_PREVIEW,
    SOURCE_CONFIRM,
    SOURCE_LIST,
    SOURCE_CANCEL,
    SOURCE_PROFILE_PREVIEW,
    SOURCE_PROFILE_DOWNLOAD,
    SOURCE_DELIVERIES,
    SOURCE_DELIVERY_CANCEL,
    SITE_CATALOG,
    SITE_RECOGNIZE,
    LIBRARY_STATUS,
    LIBRARY_MIGRATE,
    LIBRARY_SWITCH,
    LIBRARY_CANCEL,
    LIBRARY_RELEASE_OLD,
    NETWORK_GET,
    NETWORK_SET,
    ARCHIVE_ENTRY_SYNC,
    ARCHIVE_RECHECK,
    DOWNLOAD_SETTINGS,
    DOWNLOAD_SETTINGS_SET,
    DOWNLOAD_EXPORT,
    DOWNLOAD_CHECK,
    DOWNLOAD_COPIES,
    DOWNLOAD_SYNC,
    DOWNLOAD_FORGET,
    EXPLORER_TREE,
    EXPLORER_LIST,
    EXPLORER_OPEN,
    EXPLORER_REVEAL,
    ARCHIVE_TAGS,
    CREATOR_MERGE,
    CREATOR_MERGE_PREVIEW,
    CREATOR_SPLIT,
    CREATOR_SPLIT_PREVIEW,
    CREATOR_RELINK,
    CREATOR_RELINK_PREVIEW,
    CREATOR_OPERATIONS,
    IDENTITY_CANDIDATES,
    IDENTITY_CONFIRM,
    IDENTITY_REJECT,
    ACCOUNT_PROFILE_HISTORY,
    LIVE_REPLAY_CANDIDATES,
    LIVE_REPLAY_LINK,
    LIVE_REPLAY_REJECT,
    LIVE_REPLAY_LINKS,
    IMPORT_INSPECT,
    IMPORT_START,
    IMPORT_STATUS,
    IMPORT_CANCEL,
    IMPORT_RESUME,
    IMPORT_ASSIGN,
    IMPORT_CHECK,
    RESOURCE_DUPLICATES,
    RESOURCE_SIMILAR,
    RESOURCE_COMPARE,
    RESOURCE_COMPARE_DECIDE,
    WORK_GROUP_CREATE,
    WORK_GROUP_UPDATE,
    WORK_GROUP_DELETE,
    WORK_GROUP_ADD,
    WORK_GROUP_REMOVE,
    WORK_GROUP_LIST,
    WORK_GROUP_GET,
    WORK_COMPARE,
    WORK_BATCH_PREVIEW,
    WORK_BATCH_APPLY,
    PLUGIN_INSTALL,
    PLUGIN_ACTIVATE,
    PLUGIN_UPGRADE,
    PLUGIN_ROLLBACK,
    PLUGIN_REMOVE,
    PLUGIN_RELEASES,
    PLUGIN_CAPABILITIES,
    PLUGIN_CAPABILITY_SET,
    TOOL_STATUS,
    TOOL_CHECK_UPDATE,
    TOOL_APPLY_UPDATE,
    TOOL_ROLLBACK,
    MANAGEMENT_OPERATIONS,
];

pub fn is_ui_command(command: &str) -> bool {
    UI_COMMANDS.contains(&command)
}

#[cfg(test)]
mod ui_command_tests {
    use super::*;

    #[test]
    fn ui_whitelist_is_unique_and_excludes_desktop_only_commands() {
        let mut seen = std::collections::HashSet::new();
        assert!(
            UI_COMMANDS.iter().all(|c| seen.insert(*c)),
            "duplicate command"
        );
        assert_eq!(UI_COMMANDS.len(), 140);
        for blocked in [
            DAEMON_SHUTDOWN,
            MEDIA_LEASE,
            MEDIA_READ,
            MEDIA_RELEASE,
            "daemon.nope",
        ] {
            assert!(
                !is_ui_command(blocked),
                "{blocked} must stay desktop-internal"
            );
        }
        assert!(is_ui_command(SOURCE_PREVIEW));
    }
}
