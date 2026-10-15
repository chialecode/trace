/**
 * 控制协议调用封装（经 Tauri 命令薄转发到 daemon）。
 * 参数名与 apps/desktop/src-tauri/src/commands.rs 对齐（camelCase 自动转换）。
 */
import { invoke } from "@tauri-apps/api/core";

export interface LiveDto {session_id:string;state:string;generation:number;manual_stopped:boolean;started_at:string;ended_at?:string;stop_reason?:string}
export interface FragmentDto {authentication?:AuthenticationState;actual_spec?:MediaSpecDto;captured_at?:string|null;saved_at?:string|null;quality?:{requested_quality?:QualityRequirement;actual_quality?:{height?:number;fps?:number};below_target?:boolean|number};sequence:number;state:string;start_ms?:number;end_ms?:number;reason?:string;blob_id?:string;mime?:string;available?:boolean}
export interface RevisionDto { revision_id:string; title?:string; completeness:string; observed_at?:string; content_inline?:boolean; body?:string; document?:DocumentDto; metadata?:Record<string,unknown> }
export interface EntryDetailDto { entry:unknown; revisions:unknown[]; observations:unknown[]; media?:MediaDto[]; live?:LiveDto; fragments?:FragmentDto[]; revisions_cursor?:string|null; observations_cursor?:string|null; media_cursor?:string|null }
export interface QualityVariantDto {variant_id:string;blob_id:string;role:string;measured?:{width?:number;height?:number;fps?:number};reason?:string;pinned:boolean;retained:boolean;selected:boolean;eligible_at?:string}

export interface DaemonStatus {
  budgets?: {metadata_min_interval_ms:number;metadata_jitter_ms:number;scan_page_budget:number;live_concurrency:number;check_jitter_percent:number};
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

export interface SearchScope { creator_id?:string;namespace?:string;from?:string;to?:string;cursor?:string }
export interface SearchItemDto { doc_kind:string;entry_id:string|null;revision_id:string|null;creator_id:string|null;namespace:string|null;title:string|null;snippet:string;published_at:string|null }
export interface SearchResultsDto extends Page<SearchItemDto> { mode:string;status:{indexed_seq:number;total_seq:number;lagging:boolean;rebuilding:boolean} }

export interface QualityRequirement { min_height?:number|null;min_fps?:number|null;strict?:boolean;prefer_fps?:boolean|null }
export interface CreatorDto {
  aliases?:string[];
  tags?:string[];
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
  quality?:QualityRequirement;
  credential_id?:string|null;
  config_version?:number;
  coverage?:unknown[];
  capture_chat?: boolean;
  capture_subtitles?: boolean;
  subscription_id: string;
  account_id: string;
  content_type: string;
  range: unknown;
  state: string;
  check_interval_seconds: number | null;
  next_check_at: string | null;
}

export type AuthenticationState = 'anonymous'|'authenticated'|'expired'|'unknown';
export interface AuthenticationSummary {state:AuthenticationState|'mixed';states:AuthenticationState[]}
export interface TimelineItemDto {
  authentication?:AuthenticationSummary;
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
  capability?:Record<string,unknown>;
  validated_at?:string|null;
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
  active_tasks?: number;
}

export interface DeletePreview {entry_id:string;scope:'cache'|'copies'|'archive';token:string;generation:number;variants:number;pinned_preserved:number;wait_seconds:number;revisions:number}
export interface DeletedEntry {entry_id:string;lifecycle:string;generation:number;media_suppressed:boolean;title?:string}
export interface HealthIssue {issue_key:string;category:string;detail:Record<string,unknown>;resolved_at?:string|null;first_seen_at:string;last_seen_at:string;affected_jobs:number;notification_state?:string;notification_error?:string}
export interface ResourceDto extends MediaDto {entry_id:string;revision_id:string;namespace:string;title:string;account_id?:string;account_name?:string;account_key?:string;creator_id?:string;creator_name?:string;verified_copies:number}
export interface CacheStatus {index_estimated_bytes:number;index_budget_bytes:number;index_enabled:boolean;index_budget_blocked:boolean;index_rebuilding:boolean;derived_bytes:number;derived_budget_bytes:number;derived_enabled:boolean;thumbnail_bytes:number}
export interface MediaSpecDto {width?:number|null;height?:number|null;bitrate_bps?:number|null;bit_rate?:number|null;fps?:number|null;codec?:string|null;audio_codec?:string|null}
export interface AcquisitionDto {sequence:number;acquisition_id:string;auth_state:AuthenticationState;started_at:string;finished_at:string;validated_at?:string;outcome:string;media_id?:string;variant_id?:string;measured_spec?:MediaSpecDto;current_credential_available:boolean}
export interface UpgradeResource {comparison?:string;media_id:string;role:string;required:boolean;old:{variant_id?:string;bytes?:number;spec?:MediaSpecDto;auth_state:AuthenticationState;saved_at?:string;pinned:boolean};new:{variant_id?:string;bytes?:number;spec?:MediaSpecDto;saved_at?:string}}
export interface UpgradeCandidate {resources_next?:number;resource_count?:number;sequence:number;candidate_id:string;entry_id:string;revision_id:string;state:string;reason:string;title:string;namespace:string;auth_state:AuthenticationState;acquired_at:string;complete:boolean;expected:unknown;resources:UpgradeResource[]}
export type UpgradeAction='accept'|'reject'|'keep_both';
export interface SourceOffer {title?:string;position?:number;id:string;attachment_id:string;role:string;group?:string;required:boolean;representation?:string;spec?:Record<string,string|number|null>;kind?:string;bytes?:number;segments?:{bytes?:number;sha256?:string}[];protocol?:string;quality?:string;audio_only?:boolean}
export interface SourcePreview {preview_id:string;kind:'video'|'live';platform:string;source_url:string;native_key:string;title:string;account_name?:string;authentication:AuthenticationState;validated_at?:string;expires_at:string;offers:SourceOffer[]}
export interface SourceRequest {request_id:string;platform:string;kind:'video'|'live';state:string;entry_id?:string;job_id?:string;session_id?:string;session_state?:string;session_generation?:number;title:string;authentication:AuthenticationState;tasks:{job_id:string;kind:string;state:string;error?:string}[]}
export const api = {
  sourcePreview:(request:{url:string;credential_id:string|null})=>invoke<SourcePreview>('source_preview',{request}),
  sourceConfirm:(request:{preview_id:string;selected:string[]})=>invoke<{request_id:string;entry_id?:string;job_id:string;state:string}>('source_confirm',{request}),
  sourceList:()=>invoke<{items:SourceRequest[]}>('source_list',{}),
  sourceCancel:(requestId:string)=>invoke('source_cancel',{requestId}),
  archiveAcquisitions:(entryId:string,before?:number)=>invoke<{items:AcquisitionDto[];next_before?:number;summary:AuthenticationSummary}>('archive_acquisitions',{entryId,before:before??null}),
  upgradeResources:(request:{candidate_id:string;after?:number})=>invoke<{items:UpgradeResource[];next_after?:number;total:number}>('upgrade_resources',{request}),
  upgradeList:(request:{state?:string;namespace?:string;creator_id?:string;before?:number})=>invoke<{items:UpgradeCandidate[];next_before?:number;total:number}>('upgrade_list',{request}),
  upgradeDecide:(items:{candidate_id:string;decision:UpgradeAction;expected:unknown}[])=>invoke('upgrade_decide',{items}),
  desktopClose:()=>invoke('desktop_close'),
  desktopExit:()=>invoke('desktop_exit'),
  cacheStatus:()=>invoke<CacheStatus>('cache_status'),
  cacheConfigure:(indexBudgetBytes:number,derivedBudgetBytes:number)=>invoke<CacheStatus>('cache_configure',{indexBudgetBytes,derivedBudgetBytes}),
  cacheControl:(scope:string,rebuild:boolean)=>invoke<CacheStatus>('cache_control',{scope,rebuild}),
  archiveResources:(request:{cursor?:string;namespace?:string;role?:string;creator_id?:string;available?:boolean})=>invoke<Page<ResourceDto>>("archive_resources",{request}),
  deletePreview:(entryId:string,scope:string)=>invoke<DeletePreview>('archive_delete_preview',{entryId,scope}),
  archiveDelete:(entryId:string,scope:string,token:string)=>invoke('archive_delete',{entryId,scope,token}),
  deletedEntries:(after?:string)=>invoke<{items:DeletedEntry[];next?:string|null}>('archive_deleted',{after:after??null}),
  restoreDeleted:(entryId:string,generation:number)=>invoke('archive_restore_deleted',{entryId,generation}),
  startupStatus:()=>invoke<{enabled:boolean;current_location:boolean}>("startup_status"),
  startupConfigure:(enabled:boolean)=>invoke<{enabled:boolean;current_location:boolean}>("startup_configure",{enabled}),
  healthList:()=>invoke<{items:HealthIssue[]}>("health_list"),
  healthRefs:(issueKey:string,after?:string)=>invoke<{items:{job_id:string;subscription_id?:string;active:boolean;state?:string}[];next?:string|null}>("health_refs",{issueKey,after:after??null}),
  daemonStatus: () => invoke<DaemonStatus>("daemon_status"),
  daemonPause: (paused: boolean) => invoke<DaemonStatus>("daemon_pause", { paused }),

  creatorList: () => invoke<Page<CreatorDto>>("creator_list"),
  creatorCreate: (displayName: string, notes?: string) =>
    invoke<{ creator_id: string }>("creator_create", { displayName, notes: notes ?? null }),
  creatorUpdate: (creatorId: string, displayName?: string, notes?: string, options:{aliases?:string[];tags?:string[];status?:string}={}) =>
    invoke("creator_update", { creatorId, displayName: displayName ?? null, notes: notes ?? null,aliases:options.aliases??null,tags:options.tags??null,status:options.status??null }),

  accountBind: (creatorId: string, namespace: string, nativeAccountKey: string) =>
    invoke<{ account_id: string }>("account_bind", { creatorId, namespace, nativeAccountKey }),
  accountResolve: (namespace:string,input:string,credentialId?:string) => invoke<{account_id:string;native_account_key:string;display_name?:string}>("account_resolve",{namespace,input,credentialId:credentialId??null}),
  accountHistory: (accountId:string,before?:number) => invoke<{items:{sequence:number;display_name?:string;observed_at:string;evidence:Record<string,unknown>}[];next_before?:number|null}>("account_history",{accountId,before:before??null}),
  accountList: (creatorId?: string) =>
    invoke<Page<AccountDto>>("account_list", { creatorId: creatorId ?? null }),

  subscriptionCreate: (
    accountId: string,
    rangeKind: "from_now" | "recent_days" | "all" | "date_range",
    rangeDays?: number,
    checkIntervalSeconds?: number,
    contentType = "video",
    options: {credentialId?:string;captureChat?:boolean;captureSubtitles?:boolean;quality?:QualityRequirement;start?:string;end?:string} = {},
  ) =>
    invoke<{ subscription_id: string }>("subscription_create", {
      request: {account_id: accountId,content_type:contentType,range:rangeKind==='recent_days'?{kind:rangeKind,days:rangeDays??30}:rangeKind==='date_range'?{kind:rangeKind,start:options.start??null,end:options.end??null}:{kind:rangeKind},check_interval_seconds:checkIntervalSeconds??null,credential_id:options.credentialId??null,capture_chat:options.captureChat??false,capture_subtitles:options.captureSubtitles??false,...(options.quality?{quality:options.quality}:{})},
    }),
  subscriptionUpdate: (subscriptionId:string, options:{capture_chat?:boolean;capture_subtitles?:boolean;check_interval_seconds?:number;credential_id?:string;quality?:QualityRequirement;range?:unknown}) => invoke("subscription_update", {request:{subscription_id:subscriptionId,...options}}),
  subscriptionSetEnabled: (subscriptionId: string, enabled: boolean) => invoke("subscription_set_enabled", {subscriptionId, enabled}),
  subscriptionList: (accountId?: string) =>
    invoke<Page<SubscriptionDto>>("subscription_list", { accountId: accountId ?? null }),
  subscriptionCheckNow: (subscriptionId: string) =>
    invoke<{ job_id: string }>("subscription_check_now", { subscriptionId }),

  archiveTimeline: (opts: {
    entryId?: string;
    creatorId?: string;
    authentication?: AuthenticationState;
    accountId?: string;
    kind?: string;
    saveState?: string;
    publishedFrom?:string;
    publishedBefore?:string;
    minHeight?:number;
    codec?:string;
    query?: string;
    namespace?: string;
    oldest?: boolean;
    cursor?: string;
    limit?: number;
  }) =>
    invoke<Page<TimelineItemDto>>("archive_timeline", {
      authentication:opts.authentication??null,
      entryId: opts.entryId ?? null,
      creatorId: opts.creatorId ?? null,
      accountId: opts.accountId ?? null,
      kind: opts.kind ?? null,
      saveState: opts.saveState ?? null,
      publishedFrom:opts.publishedFrom??null,
      publishedBefore:opts.publishedBefore??null,
      minHeight:opts.minHeight??null,
      codec:opts.codec??null,
      query: opts.query ?? null,
      namespace: opts.namespace ?? null,
      oldest: opts.oldest ?? false,
      cursor: opts.cursor ?? null,
      limit: opts.limit ?? 50,
    }),
  archiveEntryGet: (entryId: string) =>
    invoke<EntryDetailDto>("archive_entry_get", {
      entryId,
    }),
  archiveEntryPage: <T>(entryId:string,section:'revisions'|'observations'|'media',cursor?:string|null,revisionId?:string) => invoke<Page<T>>('archive_entry_page',{entryId,section,cursor:cursor??null,revisionId:revisionId??null}),
  archiveRevisionRead: (entryId:string,revisionId:string,field:'body'|'document'|'metadata',offset:number) => invoke<{text:string;next_offset:number|null;total_chars:number}>('archive_revision_read',{entryId,revisionId,field,offset}),
  archiveObservationRead: (entryId:string,seq:number,offset:number) => invoke<{text:string;next_offset:number|null;total_chars:number}>('archive_observation_read',{entryId,seq,offset}),
  archiveEntryRecheck: (entryId:string) => invoke('archive_entry_recheck',{entryId}),
  jobRetryFailed: (jobIds:string[]) => invoke<{requeued:number}>('job_retry_failed',{jobIds}),
  liveSetRecording: (sessionId:string, enabled:boolean, generation:number) => invoke('live_set_recording',{sessionId,enabled,generation}),
  liveFragments: (sessionId:string,before?:number) => invoke<{items:FragmentDto[];next_before?:number;total:number}>('live_fragments',{sessionId,before:before??null}),
  liveAuxiliary: (sessionId:string,before?:number) => invoke<{items:{sequence:number;source_id:string;author?:string;text:string;source_time?:string;observed_at:string}[];next_before?:number;total:number;enabled:boolean;latest?:{coverage:string;failure_class?:string;omitted:number;observed_at:string}}>('live_auxiliary',{sessionId,before:before??null}),
  qualityList: (entryId:string,after?:number) => invoke<{items:QualityVariantDto[];next_after?:number;state:string;preference:string;recycle_wait_seconds:number}>('quality_list',{entryId,after:after??null}),
  qualityConfigure: (entryId:string,preferFps:boolean,waitSeconds:number) => invoke('quality_configure',{entryId,preferFps,waitSeconds}),
  qualityPin: (variantId:string,enabled:boolean) => invoke('quality_pin',{variantId,enabled}),
  qualityRecycle: () => invoke<{moved_to_trash:number;removed_from_trash:number}>('quality_recycle'),

  searchQuery: (query: string, kind?: string, limit?: number, scope?:SearchScope) =>
    invoke<SearchResultsDto>("search_query", {request:{query,kind:kind??null,limit:limit??50,creator_id:scope?.creator_id??null,namespace:scope?.namespace??null,from:scope?.from??null,to:scope?.to??null,cursor:scope?.cursor??null}}),

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
  credentialUpdate:(credentialId:string,secret:string)=>invoke<{credential_id:string;status:string}>("credential_update",{credentialId,secret}),
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
  authentication?:AuthenticationSummary;
  approval_state?:string;
  saved_at?:string|null;
  attachment_id?: string;
  selected?: boolean;
  playback_current?: boolean;
  media_id: string; role: string; position: number; variant_id: string | null;
  blob_id: string | null; mime?: string | null; available: boolean; size: number | null;
  representation?: string; measured_spec?: string | null;
}
export interface DocumentDto {
  version: number;
  blocks: ({kind:'rich_text';spans:{text:string;bold:boolean;italic:boolean;strike:boolean}[];style:string;depth:number;ordinal?:number|null} | { kind: 'text'; text: string; style: string } | { kind: 'resource'; attachment_id: string; caption: string } | { kind: 'link'; text: string; url: string } | {kind:'list_item';text:string;depth:number;ordinal?:number|null} | {kind:'table';rows:string[][];header:boolean})[];
  omissions: string[];
}
export function mediaUrl(blobId?: string | null) {
  return blobId && /^sha256:[a-f0-9]{64}$/.test(blobId) ? `http://trace-media.localhost/blob/${blobId.slice(7)}` : '';
}
