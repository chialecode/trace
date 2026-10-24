/**
 * Control-protocol calls from the UI. Every daemon command goes through the desktop
 * process's whitelisted `daemon_call` (crates/contracts `UI_COMMANDS`); arguments have the
 * control protocol's snake_case shape. Only window and startup actions use their own
 * desktop commands.
 */
import { invoke } from "@tauri-apps/api/core";
import type {
  AccountDto,
  AcquisitionDto,
  AuthenticationState,
  AuthenticationSummary,
  CacheStatus,
  CheckReport,
  CopyView,
  CreatorDto,
  CredentialDto,
  DaemonStatus,
  DeletedEntry,
  DeletePreview,
  DeliveryView,
  DownloadSettingsView,
  DownloadTarget,
  EntryDetailDto,
  ExportReport,
  ForgetReport,
  FragmentDto,
  HealthIssue,
  ImportBatchDto,
  ImportInspectDto,
  ImportItemDto,
  IdentityCandidateDto,
  JobDto,
  LibraryStatus,
  ListView,
  ManagementOperationDto,
  MediaSyncReport,
  NetworkSetArgs,
  NetworkView,
  Page,
  PluginDto,
  PluginReleaseDto,
  CapabilityContractDto,
  ProfileDownloadReport,
  ProfilePage,
  ProfileObservationDto,
  QualityRequirement,
  QualityVariantDto,
  RecheckReport,
  Recognition,
  ReplayLinkDto,
  ResourceDto,
  ResourceCompareDto,
  SearchResultsDto,
  SearchScope,
  SimilarCandidateDto,
  SiteCatalog,
  SourcePreview,
  SourceRequest,
  SubscriptionDto,
  SyncReport,
  TagCount,
  TimelineItemDto,
  ToolStatusDto,
  TreeView,
  DuplicateGroupDto,
  UpgradeAction,
  UpgradeCandidate,
  UpgradeResource,
  WorkBatchPreviewDto,
  WorkGroupDto,
  WorkGroupMemberDto,
} from "./types";

export * from "./types";

type Args = Record<string, unknown>;

function call<T = unknown>(command: string, args: Args = {}): Promise<T> {
  return invoke<T>("daemon_call", { command, args });
}

type TextPage = { text: string; next_offset: number | null; total_chars: number };
type EnabledState = { enabled: boolean; current_location: boolean };

const timelineArgs = (opts: TimelineQuery) => ({
  authentication: opts.authentication ?? null,
  entry_id: opts.entryId ?? null,
  creator_id: opts.creatorId ?? null,
  account_id: opts.accountId ?? null,
  kind: opts.kind ?? null,
  save_state: opts.saveState ?? null,
  published_from: opts.publishedFrom ?? null,
  published_before: opts.publishedBefore ?? null,
  min_height: opts.minHeight ?? null,
  codec: opts.codec ?? null,
  query: opts.query ?? null,
  namespace: opts.namespace ?? null,
  sort: opts.sort ?? null,
  tag: opts.tag ?? null,
  content_kind: opts.contentKind ?? null,
  oldest: opts.oldest ?? false,
  cursor: opts.cursor ?? null,
  limit: opts.limit ?? 50,
});

export interface TimelineQuery {
  entryId?: string;
  creatorId?: string;
  authentication?: AuthenticationState;
  accountId?: string;
  kind?: string;
  saveState?: string;
  publishedFrom?: string;
  publishedBefore?: string;
  minHeight?: number;
  codec?: string;
  query?: string;
  namespace?: string;
  /** `published` (default), `plays`, `likes`, `comments`, `favorites` or `duration`. */
  sort?: string;
  tag?: string;
  /** `text`, `image`, `live_photo`, `video`, `audio`, `article` or `live`. */
  contentKind?: string;
  oldest?: boolean;
  cursor?: string;
  limit?: number;
}

export interface ExplorerQuery {
  node: string;
  sort?: string;
  descending?: boolean;
  query?: string;
  kind?: string;
  state?: string;
  offset?: number;
  limit?: number;
}

export interface SubscriptionOptions {
  autoDownload?: boolean;
  credentialId?: string;
  captureChat?: boolean;
  captureSubtitles?: boolean;
  quality?: Partial<QualityRequirement>;
  start?: string;
  end?: string;
}

type RangeKind = "from_now" | "recent_days" | "all" | "date_range";

function subscriptionRange(
  kind: RangeKind,
  days: number | undefined,
  options: SubscriptionOptions,
) {
  if (kind === "recent_days") return { kind, days: days ?? 30 };
  if (kind === "date_range")
    return { kind, start: options.start ?? null, end: options.end ?? null };
  return { kind };
}

export const api = {
  // ---- one-source download ----
  sourcePreview: (request: { url: string; credential_id: string | null }) =>
    call<SourcePreview>("source.preview", request),
  sourceConfirm: (request: {
    preview_id: string;
    selected: string[];
    destination?: DownloadTarget;
  }) =>
    call<{ request_id: string; entry_id?: string; job_id: string; state: string }>(
      "source.confirm",
      request,
    ),
  sourceList: () => call<{ items: SourceRequest[] }>("source.list"),
  sourceCancel: (requestId: string) => call("source.cancel", { request_id: requestId }),

  // ---- sites, profile selection and deliveries ----
  siteCatalog: () => call<SiteCatalog>("site.catalog"),
  siteRecognize: (url: string) => call<Recognition>("site.recognize", { url }),
  profilePreview: (request: {
    url?: string;
    previewId?: string;
    ruleId?: string;
    credentialId?: string;
  }) =>
    call<ProfilePage>("source.profile.preview", {
      url: request.url ?? null,
      preview_id: request.previewId ?? null,
      rule_id: request.ruleId ?? null,
      credential_id: request.credentialId ?? null,
    }),
  profileDownload: (previewId: string, nativeKeys: string[], target: DownloadTarget) =>
    call<ProfileDownloadReport>("source.profile.download", {
      preview_id: previewId,
      native_keys: nativeKeys,
      target,
    }),
  deliveries: (deliveryId?: string, limit?: number) =>
    call<{ items: DeliveryView[] }>("source.deliveries", {
      delivery_id: deliveryId ?? null,
      limit: limit ?? null,
    }),
  deliveryCancel: (deliveryId: string) =>
    call("source.delivery.cancel", { delivery_id: deliveryId }),

  // ---- library location, download folders and proxy ----
  libraryStatus: () => call<LibraryStatus>("library.status"),
  libraryMigrate: (toPath: string) => call<LibraryStatus>("library.migrate", { to_path: toPath }),
  librarySwitch: () => call<LibraryStatus>("library.switch"),
  libraryCancel: () => call<LibraryStatus>("library.cancel"),
  libraryReleaseOld: (locationId: string) =>
    call<LibraryStatus>("library.release_old", { location_id: locationId }),
  downloadSettings: () => call<DownloadSettingsView>("download.settings"),
  downloadSettingsSet: (settings: { default_dir?: string; sidecar?: boolean }) =>
    call<DownloadSettingsView>("download.settings_set", {
      default_dir: settings.default_dir ?? null,
      sidecar: settings.sidecar ?? null,
    }),
  downloadExport: (entryIds: string[], dir?: string) =>
    call<ExportReport>("download.export", { entry_ids: entryIds, dir: dir ?? null }),
  downloadCheck: (scope: { locationId?: string; entryId?: string }) =>
    call<CheckReport>("download.check", {
      location_id: scope.locationId ?? null,
      entry_id: scope.entryId ?? null,
    }),
  downloadCopies: (entryId: string) =>
    call<{ items: CopyView[] }>("download.copies", { entry_id: entryId }),
  downloadSync: (entryIds: string[]) => call<SyncReport>("download.sync", { entry_ids: entryIds }),
  downloadForget: (entryId: string, locationId?: string) =>
    call<ForgetReport>("download.forget", { entry_id: entryId, location_id: locationId ?? null }),
  networkGet: () => call<NetworkView>("network.get"),
  networkSet: (args: Partial<NetworkSetArgs> & { scope: string; mode: string }) =>
    call<NetworkView>("network.set", {
      proxy_url: null,
      username: null,
      password: null,
      keep_login: false,
      ...args,
    }),

  // ---- explorer ----
  explorerTree: (node?: string) => call<TreeView>("explorer.tree", { node: node ?? null }),
  explorerList: (q: ExplorerQuery) =>
    call<ListView>("explorer.list", {
      node: q.node,
      sort: q.sort ?? null,
      descending: q.descending ?? false,
      query: q.query?.trim() || null,
      kind: q.kind || null,
      state: q.state || null,
      offset: q.offset ?? 0,
      limit: q.limit ?? 100,
    }),
  explorerOpen: (locationId: string, relPath: string) =>
    call("explorer.open", { location_id: locationId, rel_path: relPath }),
  explorerReveal: (locationId: string, relPath: string) =>
    call("explorer.reveal", { location_id: locationId, rel_path: relPath }),

  // ---- entries: undownloaded files and unified tags ----
  archiveEntrySync: (entryIds: string[]) =>
    call<MediaSyncReport>("archive.entry.sync", { entry_ids: entryIds }),
  archiveRecheck: (request: {
    entryIds?: string[];
    from?: string;
    to?: string;
    accountId?: string;
    failed?: boolean;
    limit?: number;
  }) =>
    call<RecheckReport>("archive.recheck", {
      entry_ids: request.entryIds ?? [],
      from: request.from ?? null,
      to: request.to ?? null,
      account_id: request.accountId ?? null,
      failed: request.failed ?? false,
      limit: request.limit ?? null,
    }),
  archiveTags: (scope: {
    creatorId?: string;
    accountId?: string;
    namespace?: string;
    prefix?: string;
    limit?: number;
  }) =>
    call<TagCount[]>("archive.tags", {
      creator_id: scope.creatorId ?? null,
      account_id: scope.accountId ?? null,
      namespace: scope.namespace ?? null,
      prefix: scope.prefix ?? null,
      limit: scope.limit ?? null,
    }),

  // ---- archive history ----
  archiveAcquisitions: (entryId: string, before?: number) =>
    call<{
      items: AcquisitionDto[];
      next_before?: number;
      summary: AuthenticationSummary;
    }>("archive.acquisitions", { entry_id: entryId, before: before ?? null }),
  upgradeResources: (request: { candidate_id: string; after?: number }) =>
    call<{ items: UpgradeResource[]; next_after?: number; total: number }>(
      "upgrade.resources",
      request,
    ),
  upgradeList: (request: {
    state?: string;
    namespace?: string;
    creator_id?: string;
    before?: number;
  }) =>
    call<{ items: UpgradeCandidate[]; next_before?: number; total: number }>(
      "upgrade.list",
      request,
    ),
  upgradeDecide: (items: { candidate_id: string; decision: UpgradeAction; expected: unknown }[]) =>
    call("upgrade.decide", { items }),

  // ---- desktop process ----
  desktopClose: () => invoke("desktop_close"),
  desktopExit: () => invoke("desktop_exit"),
  startupStatus: () => invoke<EnabledState>("startup_status"),
  startupConfigure: (enabled: boolean) => invoke<EnabledState>("startup_configure", { enabled }),

  // ---- cache and storage ----
  cacheStatus: () => call<CacheStatus>("cache.status"),
  cacheConfigure: (indexBudgetBytes: number, derivedBudgetBytes: number) =>
    call<CacheStatus>("cache.configure", {
      index_budget_bytes: indexBudgetBytes,
      derived_budget_bytes: derivedBudgetBytes,
    }),
  cacheControl: (scope: string, rebuild: boolean) =>
    call<CacheStatus>("cache.control", { scope, rebuild }),
  archiveResources: (request: {
    cursor?: string;
    namespace?: string;
    role?: string;
    creator_id?: string;
    available?: boolean;
  }) => call<Page<ResourceDto>>("archive.resources", request),
  deletePreview: (entryId: string, scope: string) =>
    call<DeletePreview>("archive.delete_preview", { entry_id: entryId, scope }),
  archiveDelete: (entryId: string, scope: string, token: string) =>
    call("archive.delete", { entry_id: entryId, scope, token }),
  deletedEntries: (after?: string) =>
    call<{ items: DeletedEntry[]; next?: string | null }>("archive.deleted", {
      after: after ?? null,
    }),
  restoreDeleted: (entryId: string, generation: number) =>
    call("archive.restore_deleted", { entry_id: entryId, generation }),
  healthList: () => call<{ items: HealthIssue[] }>("health.list"),
  healthRefs: (issueKey: string, after?: string) =>
    call<{
      items: { job_id: string; subscription_id?: string; active: boolean; state?: string }[];
      next?: string | null;
    }>("health.refs", { issue_key: issueKey, after: after ?? null }),
  storageUsage: () => call<Record<string, unknown>>("storage.usage"),
  exportStart: (targetDir: string, includeMedia: boolean, creatorId?: string) =>
    call<{ accepted: boolean; target: string }>("storage.export.start", {
      target_dir: targetDir,
      include_media: includeMedia,
      creator_id: creatorId ?? null,
    }),
  backupStart: () => call<{ accepted: boolean; job_id: string }>("storage.backup.start"),
  backupList: () => call<Page<Record<string, unknown>>>("storage.backup.list"),
  restoreStart: (backupRoot: string, targetRoot: string) =>
    call<{ profile_path: string; restored_objects: number; missing_objects: number }>(
      "storage.restore.start",
      { backup_root: backupRoot, target_root: targetRoot },
    ),

  // ---- daemon ----
  daemonStatus: () => call<DaemonStatus>("daemon.status"),
  daemonPause: (paused: boolean) => call<DaemonStatus>("daemon.pause", { paused }),

  // ---- creators, accounts and subscriptions ----
  creatorList: () => call<Page<CreatorDto>>("creator.list", { include_archived: true }),
  creatorCreate: (displayName: string, notes?: string) =>
    call<{ creator_id: string }>("creator.create", {
      display_name: displayName,
      aliases: [],
      tags: [],
      notes: notes ?? "",
    }),
  creatorUpdate: (
    creatorId: string,
    displayName?: string,
    notes?: string,
    options: { aliases?: string[]; tags?: string[]; status?: string } = {},
  ) =>
    call("creator.update", {
      creator_id: creatorId,
      display_name: displayName ?? null,
      notes: notes ?? null,
      aliases: options.aliases ?? null,
      tags: options.tags ?? null,
      status: options.status ?? null,
    }),
  accountBind: (creatorId: string, namespace: string, nativeAccountKey: string) =>
    call<{ account_id: string }>("account.bind", {
      creator_id: creatorId,
      namespace,
      native_account_key: nativeAccountKey,
    }),
  accountResolve: (namespace: string, input: string, credentialId?: string) =>
    call<{ account_id: string; native_account_key: string; display_name?: string }>(
      "account.resolve",
      { namespace, input, credential_id: credentialId ?? null },
    ),
  accountHistory: (accountId: string, before?: number) =>
    call<{
      items: {
        sequence: number;
        display_name?: string;
        observed_at: string;
        evidence: Record<string, unknown>;
      }[];
      next_before?: number | null;
    }>("account.history", { account_id: accountId, before: before ?? null }),
  accountList: (creatorId?: string) =>
    call<Page<AccountDto>>("account.list", { creator_id: creatorId ?? null }),
  subscriptionCreate: (
    accountId: string,
    rangeKind: RangeKind,
    rangeDays?: number,
    checkIntervalSeconds?: number,
    contentType = "video",
    options: SubscriptionOptions = {},
  ) =>
    call<{ subscription_id: string }>("subscription.create", {
      account_id: accountId,
      content_type: contentType,
      range: subscriptionRange(rangeKind, rangeDays, options),
      check_interval_seconds: checkIntervalSeconds ?? null,
      credential_id: options.credentialId ?? null,
      capture_chat: options.captureChat ?? false,
      capture_subtitles: options.captureSubtitles ?? false,
      ...(options.autoDownload === undefined ? {} : { auto_download: options.autoDownload }),
      ...(options.quality ? { quality: options.quality } : {}),
    }),
  subscriptionUpdate: (
    subscriptionId: string,
    options: {
      capture_chat?: boolean;
      capture_subtitles?: boolean;
      auto_download?: boolean;
      check_interval_seconds?: number;
      credential_id?: string;
      quality?: Partial<QualityRequirement>;
      range?: unknown;
    },
  ) => call("subscription.update", { subscription_id: subscriptionId, ...options }),
  subscriptionSetEnabled: (subscriptionId: string, enabled: boolean) =>
    call("subscription.update", { subscription_id: subscriptionId, enabled }),
  subscriptionList: (accountId?: string) =>
    call<Page<SubscriptionDto>>("subscription.list", { account_id: accountId ?? null }),
  subscriptionCheckNow: (subscriptionId: string) =>
    call<{ job_id: string }>("subscription.check_now", { subscription_id: subscriptionId }),

  // ---- timeline, entries and search ----
  archiveTimeline: (opts: TimelineQuery) =>
    call<Page<TimelineItemDto>>("archive.timeline", timelineArgs(opts)),
  archiveEntryGet: (entryId: string) =>
    call<EntryDetailDto>("archive.entry.get", { entry_id: entryId }),
  archiveEntryPage: <T>(
    entryId: string,
    section: "revisions" | "observations" | "media",
    cursor?: string | null,
    revisionId?: string,
  ) =>
    call<Page<T>>("archive.entry.page", {
      entry_id: entryId,
      section,
      revision_id: revisionId ?? null,
      cursor: cursor ?? null,
    }),
  archiveRevisionRead: (
    entryId: string,
    revisionId: string,
    field: "body" | "document" | "metadata",
    offset: number,
  ) =>
    call<TextPage>("archive.revision.read", {
      entry_id: entryId,
      revision_id: revisionId,
      field,
      offset,
    }),
  archiveObservationRead: (entryId: string, seq: number, offset: number) =>
    call<TextPage>("archive.observation.read", { entry_id: entryId, seq, offset }),
  archiveEntryRecheck: (entryId: string) => call("archive.entry.recheck", { entry_id: entryId }),
  searchQuery: (query: string, kind?: string, limit?: number, scope?: SearchScope) =>
    call<SearchResultsDto>("search.query", {
      query,
      kind: kind ?? null,
      limit: limit ?? 50,
      creator_id: scope?.creator_id ?? null,
      namespace: scope?.namespace ?? null,
      from: scope?.from ?? null,
      to: scope?.to ?? null,
      cursor: scope?.cursor ?? null,
    }),

  // ---- jobs ----
  jobList: (stateFilter?: string, jobType?: string, limit?: number) =>
    call<Page<JobDto>>("job.list", {
      state: stateFilter ?? null,
      job_type: jobType ?? null,
      limit: limit ?? 100,
    }),
  jobGet: (jobId: string) => call<JobDto>("job.get", { job_id: jobId }),
  jobRetry: (jobId: string) => call("job.retry", { job_id: jobId }),
  jobRetryFailed: (jobIds: string[]) =>
    call<{ requeued: number }>("job.retry_failed", { job_ids: jobIds }),
  jobCancel: (jobId: string) => call("job.cancel", { job_id: jobId }),

  // ---- live recording and quality ----
  liveSetRecording: (sessionId: string, enabled: boolean, generation: number) =>
    call("live.set_recording", { session_id: sessionId, enabled, generation }),
  liveFragments: (sessionId: string, before?: number) =>
    call<{ items: FragmentDto[]; next_before?: number; total: number }>("live.fragments", {
      session_id: sessionId,
      before: before ?? null,
    }),
  liveAuxiliary: (sessionId: string, before?: number) =>
    call<{
      items: {
        sequence: number;
        source_id: string;
        author?: string;
        text: string;
        source_time?: string;
        observed_at: string;
      }[];
      next_before?: number;
      total: number;
      enabled: boolean;
      latest?: { coverage: string; failure_class?: string; omitted: number; observed_at: string };
    }>("live.auxiliary", { session_id: sessionId, before: before ?? null }),
  qualityList: (entryId: string, after?: number) =>
    call<{
      items: QualityVariantDto[];
      next_after?: number;
      state: string;
      preference: string;
      recycle_wait_seconds: number;
    }>("quality.list", { entry_id: entryId, after: after ?? null }),
  qualityListFiltered: (state?: string, after?: string, limit = 50) =>
    call<{
      items: Record<string, unknown>[];
      next_after?: string | null;
      reason?: string;
      state?: string;
      preference?: string;
      recycle_wait_seconds?: number;
    }>("quality.list", { entry_id: null, state: state ?? null, after: after ?? null, limit }),
  qualityConfigure: (entryId: string, preferFps: boolean, waitSeconds: number) =>
    call<{ updated: boolean; scope: Record<string, unknown>; policy: Record<string, unknown> }>(
      "quality.configure",
      {
        entry_id: entryId || null,
        prefer_fps: preferFps,
        wait_seconds: waitSeconds,
      },
    ),
  qualityPin: (variantId: string, enabled: boolean) =>
    call<{ pinned: boolean }>("quality.pin", { variant_id: variantId, enabled }),
  qualityPinEntry: (entryId: string, enabled: boolean) =>
    call<{ pinned: boolean }>("quality.pin", { entry_id: entryId, enabled }),
  qualityRecycle: () =>
    call<{ moved_to_trash: number; removed_from_trash: number }>("quality.recycle"),

  // ---- credentials and modules ----
  credentialList: (platform?: string) =>
    call<Page<CredentialDto>>("credential.list", { platform: platform ?? null }),
  credentialUpdate: (credentialId: string, secret: string) =>
    call<{ credential_id: string; status: string }>("credential.update", {
      credential_id: credentialId,
      secret,
    }),
  credentialImport: (platform: string, subject: string | null, secret: string) =>
    call<{ credential_id: string; status: string }>("credential.import", {
      platform,
      subject,
      secret,
    }),
  credentialValidate: (credentialId: string) =>
    call<{ status: string; capability: unknown }>("credential.validate", {
      credential_id: credentialId,
    }),
  credentialRevoke: (credentialId: string) =>
    call<{ ok: boolean }>("credential.revoke", { credential_id: credentialId }),
  pluginList: () => call<Page<PluginDto>>("plugin.list"),
  pluginSetState: (pluginId: string, enabled: boolean) =>
    call("plugin.set_state", { plugin_id: pluginId, state: enabled ? "enabled" : "disabled" }),

  // ---- management: creator relations, identity, imports, comparisons, replays, work groups ----
  creatorMergePreview: (sourceCreatorId: string, targetCreatorId: string) =>
    call<{ impact: Record<string, unknown>; preview_token: string }>("creator.merge_preview", {
      source_creator_id: sourceCreatorId,
      target_creator_id: targetCreatorId,
    }),
  creatorMerge: (sourceCreatorId: string, targetCreatorId: string, previewToken: string) =>
    call<Record<string, unknown>>("creator.merge", {
      source_creator_id: sourceCreatorId,
      target_creator_id: targetCreatorId,
      preview_token: previewToken,
    }),
  creatorSplitPreview: (sourceCreatorId: string, accountIds: string[], displayName?: string) =>
    call<{ impact: Record<string, unknown>; preview_token: string }>("creator.split_preview", {
      source_creator_id: sourceCreatorId,
      account_ids: accountIds,
      display_name: displayName ?? null,
    }),
  creatorSplit: (
    sourceCreatorId: string,
    accountIds: string[],
    previewToken: string,
    displayName?: string,
  ) =>
    call<{ creator_id: string; accounts: string[] }>("creator.split", {
      source_creator_id: sourceCreatorId,
      account_ids: accountIds,
      preview_token: previewToken,
      display_name: displayName ?? null,
    }),
  creatorRelinkPreview: (accountId: string, expectedCreatorId: string, targetCreatorId: string) =>
    call<{ impact: Record<string, unknown>; preview_token: string }>("creator.relink_preview", {
      account_id: accountId,
      expected_creator_id: expectedCreatorId,
      target_creator_id: targetCreatorId,
    }),
  creatorRelink: (
    accountId: string,
    expectedCreatorId: string,
    targetCreatorId: string,
    previewToken: string,
  ) =>
    call<Record<string, unknown>>("creator.relink", {
      account_id: accountId,
      expected_creator_id: expectedCreatorId,
      target_creator_id: targetCreatorId,
      preview_token: previewToken,
    }),
  creatorOperations: (creatorId?: string, after?: string, limit = 50) =>
    call<{ items: ManagementOperationDto[]; next_after?: string | null }>("creator.operations", {
      creator_id: creatorId ?? null,
      after: after ?? null,
      limit,
    }),
  identityCandidates: (scope: {
    accountId?: string;
    creatorId?: string;
    state?: string;
    after?: string;
    limit?: number;
  }) =>
    call<{ items: IdentityCandidateDto[]; next_after?: string | null }>("identity.candidates", {
      account_id: scope.accountId ?? null,
      creator_id: scope.creatorId ?? null,
      state: scope.state ?? null,
      after: scope.after ?? null,
      limit: scope.limit ?? 50,
    }),
  identityConfirm: (candidateId: string) =>
    call<Record<string, unknown>>("identity.confirm", { candidate_id: candidateId }),
  identityReject: (candidateId: string) =>
    call<Record<string, unknown>>("identity.reject", { candidate_id: candidateId }),
  accountProfileHistory: (accountId: string, before?: number, limit = 20) =>
    call<{ items: ProfileObservationDto[]; next_before?: number | null }>(
      "account.profile_history",
      { account_id: accountId, before: before ?? null, limit },
    ),
  importInspect: (
    path: string,
    mode: "managed" | "in_place" = "managed",
    targetCreatorId?: string,
    followLinks = false,
  ) =>
    call<ImportInspectDto>("import.inspect", {
      path,
      mode,
      target_creator_id: targetCreatorId ?? null,
      follow_links: followLinks,
    }),
  importStart: (
    path: string,
    mode: "managed" | "in_place",
    previewToken: string,
    targetCreatorId?: string,
  ) =>
    call<ImportBatchDto>("import.start", {
      path,
      mode,
      preview_token: previewToken,
      target_creator_id: targetCreatorId ?? null,
      follow_links: false,
    }),
  importStatus: (batchId?: string, after = -1, limit = 100) =>
    call<{
      batches?: ImportBatchDto[];
      batch?: ImportBatchDto | null;
      items: ImportItemDto[];
      next_after?: number | null;
      latest?: unknown;
    }>("import.status", { batch_id: batchId ?? null, after, limit }),
  importCancel: (batchId: string) =>
    call<{ cancelled: boolean }>("import.cancel", { batch_id: batchId }),
  importResume: (batchId: string) =>
    call<{ resumed: boolean; items_reset: number }>("import.resume", { batch_id: batchId }),
  importAssign: (batchId: string, targetCreatorId: string, itemIds?: string[]) =>
    call<{ assigned: number }>("import.assign", {
      batch_id: batchId,
      target_creator_id: targetCreatorId,
      item_ids: itemIds ?? null,
    }),
  importCheck: (batchId?: string) =>
    call<{ checked: number; unchanged: number; changed: number; offline: number }>("import.check", {
      batch_id: batchId ?? null,
    }),
  liveReplayCandidates: (sessionId: string) =>
    call<{ items: Record<string, unknown>[] }>("live.replay.candidates", { session_id: sessionId }),
  liveReplayLink: (sessionId: string, entryId: string, decision = "keep_both") =>
    call<ReplayLinkDto>("live.replay.link", {
      session_id: sessionId,
      entry_id: entryId,
      decision,
    }),
  liveReplayReject: (sessionId: string, entryId: string, reason?: string) =>
    call<Record<string, unknown>>("live.replay.reject", {
      session_id: sessionId,
      entry_id: entryId,
      reason: reason ?? null,
    }),
  liveReplayLinks: (scope: { sessionId?: string; entryId?: string; state?: string }) =>
    call<{ items: ReplayLinkDto[] }>("live.replay.links", {
      session_id: scope.sessionId ?? null,
      entry_id: scope.entryId ?? null,
      state: scope.state ?? null,
    }),
  resourceDuplicates: (after?: string, limit = 50) =>
    call<{ items: DuplicateGroupDto[]; next_after?: string | null }>("resource.duplicates", {
      after: after ?? null,
      limit,
    }),
  resourceSimilar: (entryId: string, mediaId?: string, limit = 20) =>
    call<{ items: SimilarCandidateDto[]; incomparable?: string | null }>("resource.similar", {
      entry_id: entryId,
      media_id: mediaId ?? null,
      limit,
    }),
  resourceCompare: (leftEntryId: string, rightEntryId: string) =>
    call<ResourceCompareDto>("resource.compare", {
      left_entry_id: leftEntryId,
      right_entry_id: rightEntryId,
    }),
  resourceCompareDecide: (
    leftEntryId: string,
    rightEntryId: string,
    decision: "not_duplicate" | "keep_both" | "different_versions",
  ) =>
    call<Record<string, unknown>>("resource.compare.decide", {
      left_entry_id: leftEntryId,
      right_entry_id: rightEntryId,
      decision,
    }),
  workGroupCreate: (title: string, description = "") =>
    call<{ group_id: string }>("work.group.create", { title, description }),
  workGroupUpdate: (groupId: string, title?: string, description?: string) =>
    call<Record<string, unknown>>("work.group.update", {
      group_id: groupId,
      title: title ?? null,
      description: description ?? null,
    }),
  workGroupDelete: (groupId: string) =>
    call<Record<string, unknown>>("work.group.delete", { group_id: groupId }),
  workGroupAdd: (groupId: string, entryIds: string[]) =>
    call<{ added: number }>("work.group.add", { group_id: groupId, entry_ids: entryIds }),
  workGroupRemove: (groupId: string, entryIds: string[]) =>
    call<{ removed: number }>("work.group.remove", { group_id: groupId, entry_ids: entryIds }),
  workGroupList: (after?: string, limit = 50) =>
    call<{ items: WorkGroupDto[]; next_after?: string | null }>("work.group.list", {
      after: after ?? null,
      limit,
    }),
  workGroupGet: (groupId: string) =>
    call<{ group: WorkGroupDto; members: WorkGroupMemberDto[] }>("work.group.get", {
      group_id: groupId,
    }),
  workCompare: (groupId: string) =>
    call<{ group_id: string; items: WorkGroupMemberDto[] }>("work.compare", { group_id: groupId }),
  workBatchPreview: (entryIds: string[], actions: Record<string, unknown>) =>
    call<WorkBatchPreviewDto>("work.batch.preview", { entry_ids: entryIds, actions }),
  workBatchApply: (token: string) =>
    call<{ token: string; ok: boolean; results: Record<string, unknown>[] }>("work.batch.apply", {
      token,
    }),
  managementOperations: (after?: string, limit = 50) =>
    call<{ items: Record<string, unknown>[]; next_after?: string | null }>(
      "management.operations",
      {
        after: after ?? null,
        limit,
      },
    ),

  // ---- plugins, capabilities and the manual tool update ----
  pluginInstall: (sourceDir: string) =>
    call<Record<string, unknown>>("plugin.install", { source_dir: sourceDir }),
  pluginActivate: (pluginId: string, version: string) =>
    call<Record<string, unknown>>("plugin.activate", { plugin_id: pluginId, version }),
  pluginUpgrade: (sourceDir: string) =>
    call<Record<string, unknown>>("plugin.upgrade", { source_dir: sourceDir }),
  pluginRollback: (pluginId: string) =>
    call<Record<string, unknown>>("plugin.rollback", { plugin_id: pluginId }),
  pluginRemove: (pluginId: string, version: string) =>
    call<Record<string, unknown>>("plugin.remove", { plugin_id: pluginId, version }),
  pluginReleases: (pluginId?: string) =>
    call<{ items: PluginReleaseDto[] }>("plugin.releases", { plugin_id: pluginId ?? null }),
  pluginCapabilities: () =>
    call<{ contracts: CapabilityContractDto[]; active: Record<string, unknown>[] }>(
      "plugin.capabilities",
    ),
  pluginCapabilitySet: (pluginId: string, kind: string, enabled: boolean) =>
    call<Record<string, unknown>>("plugin.capability_set", {
      plugin_id: pluginId,
      kind,
      enabled,
    }),
  toolStatus: () => call<ToolStatusDto>("tool.status"),
  toolCheckUpdate: () =>
    call<{ latest: string; current: string; newer: boolean; checked_at: string }>(
      "tool.check_update",
    ),
  toolApplyUpdate: (version: string) => call<ToolStatusDto>("tool.apply_update", { version }),
  toolRollback: () => call<ToolStatusDto>("tool.rollback"),

  // ---- opening files ----
  openSource: (entryId: string) => call<void>("archive.open_source", { entry_id: entryId }),
  openExternal: (blobId: string) => call<void>("media.open_external", { blob_id: blobId }),
};

export function mediaUrl(blobId?: string | null) {
  return blobId && /^sha256:[a-f0-9]{64}$/.test(blobId)
    ? `http://trace-media.localhost/blob/${blobId.slice(7)}`
    : "";
}
