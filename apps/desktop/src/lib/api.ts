/**
 * 控制协议调用封装（经 Tauri 命令薄转发到 daemon）。
 * 参数名与 apps/desktop/src-tauri/src/commands.rs 对齐（camelCase 自动转换）。
 */
import { invoke } from "@tauri-apps/api/core";

export interface DaemonStatus {
  archive_id: string;
  instance_id: string;
  node_id: string;
  schema_version: number;
  paused: boolean;
  started_at: string;
  job_counts: Record<string, number>;
  storage: Record<string, unknown>;
  media_backend: { ffmpeg: string | null; ffprobe: string | null; version: string | null };
}

export interface Page<T> {
  items: T[];
  next_cursor: string | null;
  counts?: { global: number; filtered: number; scope: string };
}

export interface CreatorDto {
  creator_id: string;
  display_name: string;
  status?: string;
  notes?: string;
  created_at?: string;
}

export interface AccountDto {
  account_id: string;
  creator_id: string;
  namespace: string;
  native_account_key: string;
  display_name: string | null;
  profile_url: string | null;
}

export interface SubscriptionDto {
  subscription_id: string;
  account_id: string;
  content_type: string;
  range: unknown;
  state: string;
  check_interval_seconds: number | null;
  next_check_at: string | null;
}

export interface TimelineItemDto {
  entry_id: string;
  current_revision_id: string | null;
  title: string;
  namespace: string;
  kind: string;
  published_at: string | null;
  published_precision: string;
  first_seen_at?: string;
  discovered_at?: string;
  creator_name?: string | null;
  account_name?: string | null;
  native_account_key?: string;
  source_key?: string;
  source_url?: string | null;
  cover_blob_id?: string | null;
  access_state?: string;
  save_progress?: string;
  quality_state?: string;
  media_summary?: Record<string, unknown>;
}

export interface JobDto {
  job_id: string;
  job_key: string;
  job_type: string;
  subject_id: string;
  state: string;
  attempts: number;
  target_generation: number;
  priority: number;
  next_run_at: string | null;
  last_error: { class?: string; message?: string } | null;
  created_at: string;
  updated_at: string;
}

export interface CredentialDto {
  credential_id: string;
  platform: string;
  subject: string | null;
  status: string;
  created_at: string;
}

export interface PluginDto {
  plugin_id: string;
  platform: string;
  state: string;
  capabilities?: string[];
}

export const api = {
  daemonStatus: () => invoke<DaemonStatus>("daemon_status"),
  daemonPause: (paused: boolean) => invoke<DaemonStatus>("daemon_pause", { paused }),

  creatorList: () => invoke<Page<CreatorDto>>("creator_list"),
  creatorCreate: (displayName: string, notes?: string) =>
    invoke<{ creator_id: string }>("creator_create", { displayName, notes: notes ?? null }),
  creatorUpdate: (creatorId: string, displayName?: string, notes?: string) =>
    invoke("creator_update", { creatorId, displayName: displayName ?? null, notes: notes ?? null }),

  accountBind: (creatorId: string, namespace: string, nativeAccountKey: string) =>
    invoke<{ account_id: string }>("account_bind", { creatorId, namespace, nativeAccountKey }),
  accountList: (creatorId?: string) =>
    invoke<Page<AccountDto>>("account_list", { creatorId: creatorId ?? null }),

  subscriptionCreate: (
    accountId: string,
    rangeKind: "from_now" | "recent_days",
    rangeDays?: number,
    checkIntervalSeconds?: number,
  ) =>
    invoke<{ subscription_id: string }>("subscription_create", {
      accountId,
      rangeKind,
      rangeDays: rangeDays ?? null,
      checkIntervalSeconds: checkIntervalSeconds ?? null,
    }),
  subscriptionSetEnabled: (subscriptionId: string, enabled: boolean) => invoke("subscription_set_enabled", {subscriptionId, enabled}),
  subscriptionList: (accountId?: string) =>
    invoke<Page<SubscriptionDto>>("subscription_list", { accountId: accountId ?? null }),
  subscriptionCheckNow: (subscriptionId: string) =>
    invoke<{ job_id: string }>("subscription_check_now", { subscriptionId }),

  archiveTimeline: (opts: {
    entryId?: string;
    creatorId?: string;
    accountId?: string;
    kind?: string;
    saveState?: string;
    query?: string;
    namespace?: string;
    oldest?: boolean;
    cursor?: string;
    limit?: number;
  }) =>
    invoke<Page<TimelineItemDto>>("archive_timeline", {
      entryId: opts.entryId ?? null,
      creatorId: opts.creatorId ?? null,
      accountId: opts.accountId ?? null,
      kind: opts.kind ?? null,
      saveState: opts.saveState ?? null,
      query: opts.query ?? null,
      namespace: opts.namespace ?? null,
      oldest: opts.oldest ?? false,
      cursor: opts.cursor ?? null,
      limit: opts.limit ?? 50,
    }),
  archiveEntryGet: (entryId: string) =>
    invoke<{ entry: unknown; revisions: unknown[]; observations: unknown[]; media?: MediaDto[] }>("archive_entry_get", {
      entryId,
    }),

  searchQuery: (query: string, kind?: string, limit?: number) =>
    invoke<Page<TimelineItemDto>>("search_query", { query, kind: kind ?? null, limit: limit ?? 50 }),

  jobList: (stateFilter?: string, jobType?: string, limit?: number) =>
    invoke<Page<JobDto>>("job_list", {
      stateFilter: stateFilter ?? null,
      jobType: jobType ?? null,
      limit: limit ?? 100,
    }),
  jobGet: (jobId: string) => invoke<JobDto>("job_get", { jobId }),
  jobRetry: (jobId: string) => invoke("job_retry", { jobId }),
  jobCancel: (jobId: string) => invoke("job_cancel", { jobId }),

  credentialList: (platform?: string) =>
    invoke<Page<CredentialDto>>("credential_list", { platform: platform ?? null }),
  credentialImport: (platform: string, subject: string | null, secret: string) =>
    invoke<{ credential_id: string; status: string }>("credential_import", {
      platform,
      subject,
      secret,
    }),
  credentialValidate: (credentialId: string) =>
    invoke<{ status: string; capability: unknown }>("credential_validate", { credentialId }),
  credentialRevoke: (credentialId: string) =>
    invoke<{ ok: boolean }>("credential_revoke", { credentialId }),

  pluginList: () => invoke<Page<PluginDto>>("plugin_list"),
  pluginSetState: (pluginId: string, enabled: boolean) =>
    invoke("plugin_set_state", { pluginId, enabled }),

  storageUsage: () => invoke<Record<string, unknown>>("storage_usage"),
  exportStart: (targetDir: string, includeMedia: boolean, creatorId?: string) =>
    invoke<{ accepted: boolean; target: string }>("export_start", {
      targetDir,
      includeMedia,
      creatorId: creatorId ?? null,
    }),
  backupStart: () => invoke<{ accepted: boolean; job_id: string }>("backup_start"),
  backupList: () => invoke<Page<Record<string, unknown>>>("backup_list"),
  restoreStart: (backupRoot: string, targetRoot: string) =>
    invoke<{ profile_path: string; restored_objects: number; missing_objects: number }>("restore_start", { backupRoot, targetRoot }),

  openSource: (entryId: string) => invoke<void>("open_source", {entryId}),
  openExternal: (blobId: string) => invoke<void>("open_external", { blobId }),
};

export interface MediaDto {
  media_id: string; role: string; position: number; variant_id: string | null;
  blob_id: string | null; mime?: string | null; available: boolean; size: number | null;
  representation?: string; measured_spec?: string | null;
}
export function mediaUrl(blobId?: string | null) {
  return blobId && /^sha256:[a-f0-9]{64}$/.test(blobId) ? `http://trace-media.localhost/blob/${blobId.slice(7)}` : '';
}
