<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import {
  NButton,
  NCard,
  NCheckbox,
  NDataTable,
  NInput,
  NInputNumber,
  NSelect,
  NSwitch,
  NTabPane,
  NTabs,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { h } from "vue";
import { api, type CreatorDto, type PluginDto } from "../lib/api";

const message = useMessage();
const route = useRoute();
const tab = ref("identity");
const creators = ref<CreatorDto[]>([]);
const plugins = ref<PluginDto[]>([]);
const busy = ref(false);

function fail(error: unknown) {
  message.error(String(error));
}
async function run<T>(task: () => Promise<T>): Promise<T | undefined> {
  if (busy.value) return undefined;
  busy.value = true;
  try {
    return await task();
  } catch (error) {
    fail(error);
    return undefined;
  } finally {
    busy.value = false;
  }
}

// ---- M1/M2: creator relations, candidates and profile history ----
const sourceCreator = ref<string | null>(null);
const targetCreator = ref<string | null>(null);
const splitAccounts = ref("");
const relinkAccount = ref("");
const relinkExpected = ref<string | null>(null);
const relinkTarget = ref<string | null>(null);
const operations = ref<Record<string, unknown>[]>([]);
const candidates = ref<Record<string, unknown>[]>([]);
const historyAccount = ref("");
const history = ref<Record<string, unknown>[]>([]);
const mergePreview = ref<Record<string, unknown> | null>(null);
const splitPreview = ref<Record<string, unknown> | null>(null);
const relinkPreview = ref<Record<string, unknown> | null>(null);
const creatorOptions = computed(() =>
  creators.value.map((creator) => ({ label: creator.display_name, value: creator.creator_id })),
);
watch([sourceCreator, targetCreator, splitAccounts], () => {
  mergePreview.value = null;
  splitPreview.value = null;
});
watch([relinkAccount, relinkExpected, relinkTarget], () => {
  relinkPreview.value = null;
});

async function loadCreators() {
  creators.value = (await api.creatorList()).items;
}
function splitAccountIds(): string[] {
  return splitAccounts.value
    .split(/[\s,]+/)
    .map((value) => value.trim())
    .filter(Boolean);
}
async function previewMerge() {
  if (!sourceCreator.value || !targetCreator.value) return;
  const result = await run(() =>
    api.creatorMergePreview(sourceCreator.value!, targetCreator.value!),
  );
  if (result) {
    mergePreview.value = result as unknown as Record<string, unknown>;
    message.info("已计算影响范围；核对账号、订阅和条目数量后再确认");
  }
}
async function merge() {
  const token = String(mergePreview.value?.preview_token ?? "");
  if (!sourceCreator.value || !targetCreator.value || !token) return;
  const result = await run(() =>
    api.creatorMerge(sourceCreator.value!, targetCreator.value!, token),
  );
  if (result) message.success("已合并；只移动了显式绑定，已保存内容未改动");
  mergePreview.value = null;
  await refreshIdentity();
}
async function previewSplit() {
  const accounts = splitAccountIds();
  if (!sourceCreator.value || accounts.length === 0) {
    message.warning("请选择源创作者并至少填写一个账号 ID");
    return;
  }
  const result = await run(() => api.creatorSplitPreview(sourceCreator.value!, accounts));
  if (result) {
    splitPreview.value = result as unknown as Record<string, unknown>;
    message.info("已计算受影响账号；核对后再确认拆分");
  }
}
async function split() {
  const accounts = splitAccountIds();
  const token = String(splitPreview.value?.preview_token ?? "");
  if (!sourceCreator.value || accounts.length === 0 || !token) {
    message.warning("请先预览影响范围，再按预览确认");
    return;
  }
  const result = await run(() => api.creatorSplit(sourceCreator.value!, accounts, token));
  if (result) message.success(`已拆出 ${result.accounts.length} 个账号`);
  splitPreview.value = null;
  await refreshIdentity();
}
async function previewRelink() {
  if (!relinkAccount.value || !relinkExpected.value || !relinkTarget.value) return;
  const result = await run(() =>
    api.creatorRelinkPreview(relinkAccount.value, relinkExpected.value!, relinkTarget.value!),
  );
  if (result) {
    relinkPreview.value = result as unknown as Record<string, unknown>;
    message.info("已计算账号影响范围；核对后再确认重新关联");
  }
}
async function relink() {
  const token = String(relinkPreview.value?.preview_token ?? "");
  if (!relinkAccount.value || !relinkExpected.value || !relinkTarget.value || !token) {
    message.warning("请先预览影响范围，再按预览确认");
    return;
  }
  const result = await run(() =>
    api.creatorRelink(relinkAccount.value, relinkExpected.value!, relinkTarget.value!, token),
  );
  if (result) message.success("已重新关联；期望的当前创作者与请求一致");
  relinkPreview.value = null;
  await refreshIdentity();
}
async function refreshIdentity() {
  const [ops, found] = await Promise.all([
    api.creatorOperations(),
    api.identityCandidates({ state: "pending" }),
  ]);
  operations.value = ops.items as unknown as Record<string, unknown>[];
  candidates.value = found.items as unknown as Record<string, unknown>[];
}
async function decide(candidate: Record<string, unknown>, confirm: boolean) {
  const id = String(candidate.candidate_id);
  const result = await run(() => (confirm ? api.identityConfirm(id) : api.identityReject(id)));
  if (result) message.success(confirm ? "已确认并显式绑定" : "已拒绝；资料观察仍保留");
  await refreshIdentity();
}
async function loadHistory() {
  if (!historyAccount.value.trim()) return;
  const result = await run(() => api.accountProfileHistory(historyAccount.value.trim()));
  if (result) history.value = result.items as unknown as Record<string, unknown>[];
}

// ---- M4: local imports ----
const importPath = ref("");
const importMode = ref<"managed" | "in_place">("managed");
const importInspect = ref<Record<string, unknown> | null>(null);
const importConfirm = ref(false);
const importBatch = ref<Record<string, unknown> | null>(null);
const importItems = ref<Record<string, unknown>[]>([]);
const importBatches = ref<Record<string, unknown>[]>([]);
const assignCreator = ref<string | null>(null);
watch([importPath, importMode, targetCreator], () => {
  importInspect.value = null;
  importConfirm.value = false;
});

async function inspectImport() {
  if (!importPath.value.trim()) return;
  const result = await run(() =>
    api.importInspect(importPath.value.trim(), importMode.value, targetCreator.value ?? undefined),
  );
  if (result) {
    importInspect.value = result as unknown as Record<string, unknown>;
    importConfirm.value = false;
  }
}
async function startImport() {
  const token = String(importInspect.value?.preview_token ?? "");
  if (!importPath.value.trim() || !token || !importConfirm.value) {
    message.warning("请先预览当前路径、方式和目标，并勾选确认后再开始");
    return;
  }
  const result = await run(() =>
    api.importStart(
      importPath.value.trim(),
      importMode.value,
      token,
      targetCreator.value ?? undefined,
    ),
  );
  if (result) {
    message.success("导入已排队；托管复制不改动源文件，原地索引不移动外部文件");
    importInspect.value = null;
    importConfirm.value = false;
    await refreshImports(result.batch_id);
  }
}
async function refreshImports(batchId?: string) {
  const result = await run(
    () => api.importStatus(batchId, -1, 200) as Promise<Record<string, unknown>>,
  );
  if (!result) return;
  const batches = (result.batches ?? []) as Record<string, unknown>[];
  importBatches.value = batches;
  const latest = (result.batch ?? batches[0] ?? null) as Record<string, unknown> | null;
  importBatch.value = latest;
  importItems.value = (result.items ?? []) as Record<string, unknown>[];
}
async function importAction(action: "cancel" | "resume" | "check" | "assign") {
  const id = String(importBatch.value?.batch_id ?? "");
  if (!id) return;
  const result = await run(async () => {
    if (action === "cancel") return api.importCancel(id);
    if (action === "resume") return api.importResume(id);
    if (action === "check") return api.importCheck(id);
    if (!assignCreator.value) throw new Error("请选择要归入的创作者");
    return api.importAssign(
      id,
      assignCreator.value,
      importItems.value.map((item) => String(item.item_id)),
    );
  });
  if (result) message.success("操作已提交");
  await refreshImports(id);
}

// ---- M5: duplicates, similar candidates and comparisons ----
const duplicates = ref<Record<string, unknown>[]>([]);
const similarEntry = ref("");
const similar = ref<Record<string, unknown>[]>([]);
const similarNote = ref("");
const leftId = ref("");
const rightId = ref("");
const comparison = ref<Record<string, unknown> | null>(null);

async function loadDuplicates() {
  const result = await run(() => api.resourceDuplicates());
  if (result) duplicates.value = result.items as unknown as Record<string, unknown>[];
}
async function findSimilar() {
  if (!similarEntry.value.trim()) return;
  const result = await run(() => api.resourceSimilar(similarEntry.value.trim()));
  if (result) {
    similar.value = result.items as unknown as Record<string, unknown>[];
    similarNote.value = result.incomparable ?? "";
  }
}
async function compare() {
  if (!leftId.value.trim() || !rightId.value.trim()) return;
  const result = await run(() => api.resourceCompare(leftId.value.trim(), rightId.value.trim()));
  if (result) comparison.value = result as unknown as Record<string, unknown>;
}
async function decideComparison(decision: "not_duplicate" | "keep_both" | "different_versions") {
  const result = await run(() =>
    api.resourceCompareDecide(leftId.value.trim(), rightId.value.trim(), decision),
  );
  if (result) message.success("决定已记录；没有删除、合并或替换任何对象");
}
const decisionOptions = [
  { label: "不是重复", value: "not_duplicate" },
  { label: "保留两者", value: "keep_both" },
  { label: "不同版本", value: "different_versions" },
];

// ---- M3: live replays ----
const replaySession = ref("");
const replayEntry = ref("");
const replayCandidates = ref<Record<string, unknown>[]>([]);
const replayLinks = ref<Record<string, unknown>[]>([]);
const replayChoice = ref<Record<string, unknown> | null>(null);
function rangesText(value: unknown): string {
  const ranges = Array.isArray(value) ? (value as Record<string, unknown>[]) : [];
  return ranges.length
    ? ranges.map((range) => `${range.start_ms ?? "?"}–${range.end_ms ?? "?"} ms`).join("、")
    : "无";
}
function coverageText(value: unknown): string {
  const coverage = (value ?? {}) as Record<string, unknown>;
  return coverage.complete
    ? `完整；录制 ${String(coverage.recorded_duration_ms ?? 0)} ms`
    : `不完整；${String(coverage.reason ?? "有未覆盖区间")}`;
}
function qualityText(value: unknown): string {
  if (typeof value === "string") return value;
  const quality = (value ?? {}) as Record<string, unknown>;
  return String(quality.comparison ?? quality.quality ?? "unknown");
}
function replayCoverageRanges(
  coverage: unknown,
  key: "recorded_ranges" | "unique_recorded_ranges",
): string {
  return rangesText((coverage as Record<string, unknown> | undefined)?.[key]);
}
function comparisonReason(row: Record<string, unknown>): string {
  return (
    String(row.comparison_reason ?? "") ||
    String((row.coverage as Record<string, unknown> | undefined)?.reason ?? "") ||
    (qualityText(row.quality) === "incomparable" ? "质量不可比较" : "")
  );
}
async function loadReplays() {
  if (!replaySession.value.trim()) return;
  replayChoice.value = null;
  const [found, links] = await Promise.all([
    api.liveReplayCandidates(replaySession.value.trim()),
    api.liveReplayLinks({ sessionId: replaySession.value.trim() }),
  ]);
  replayCandidates.value = found.items as Record<string, unknown>[];
  replayLinks.value = links.items as unknown as Record<string, unknown>[];
}
function selectReplay(entry: Record<string, unknown>) {
  replayChoice.value = entry;
}
function clearReplayChoice() {
  replayChoice.value = null;
}
function linkSelectedReplay(decision: "keep_both" | "replay_primary") {
  if (replayChoice.value) return linkReplay(replayChoice.value, decision);
}
function rejectSelectedReplay() {
  if (replayChoice.value) return rejectReplay(replayChoice.value);
}
async function linkReplay(entry: Record<string, unknown>, decision = "keep_both") {
  replayEntry.value = String(entry.entry_id);
  const result = await run(() =>
    api.liveReplayLink(replaySession.value.trim(), replayEntry.value, decision),
  );
  if (result) {
    const coverage = result.coverage as Record<string, unknown> | undefined;
    const quality = String(result.quality ?? "unknown");
    message.success(
      `已关联并保留全部录制；覆盖${coverage?.complete ? "完整" : "不完整"}，质量 ${quality}`,
    );
  }
  replayChoice.value = null;
  await loadReplays();
}
async function rejectReplay(entry: Record<string, unknown>) {
  const result = await run(() =>
    api.liveReplayReject(replaySession.value.trim(), String(entry.entry_id), "用户拒绝"),
  );
  if (result) message.success("已拒绝关联；没有删除任何录制");
  replayChoice.value = null;
  await loadReplays();
}

// ---- M6: quality filters ----
const qualityState = ref<string | null>(null);
const qualityItems = ref<Record<string, unknown>[]>([]);
const qualityReason = ref("");
const qualityPreferFps = ref(true);
const qualityWait = ref(0);
const qualityStates = [
  { label: "待升级", value: "pending_upgrade" },
  { label: "不可比较", value: "incomparable" },
  { label: "已替换", value: "replaced" },
  { label: "手动保留", value: "manually_kept" },
  { label: "稳定", value: "stable" },
];
async function loadQuality() {
  const result = await run(() =>
    api.qualityListFiltered(qualityState.value ?? undefined, undefined, 100),
  );
  if (result) {
    qualityItems.value = (result.items ?? []) as Record<string, unknown>[];
    qualityReason.value = String(result.reason ?? "");
    if (result.preference) qualityPreferFps.value = result.preference === "frame_rate";
    if (typeof result.recycle_wait_seconds === "number")
      qualityWait.value = result.recycle_wait_seconds;
  }
}
async function pinQuality(entry: Record<string, unknown>, enabled: boolean) {
  const result = await run(() => api.qualityPinEntry(String(entry.entry_id), enabled));
  if (result) {
    message.success(
      enabled ? "已固定该条目当前版本；固定版本不会被自动回收" : "已解除固定；回收仍按现有策略执行",
    );
  }
  await loadQuality();
}
async function reevaluate(entry: Record<string, unknown>) {
  const result = await run(() =>
    api.qualityConfigure(String(entry.entry_id), qualityPreferFps.value, qualityWait.value),
  );
  if (result) message.success("已按当前策略重新评估该条目");
  await loadQuality();
}
async function saveQuality(entryId?: string) {
  const result = await run(() =>
    api.qualityConfigure(entryId ?? "", qualityPreferFps.value, qualityWait.value),
  );
  if (result) {
    const scope = result.scope as Record<string, unknown> | undefined;
    message.success(
      entryId
        ? "质量策略已保存并重新评估该条目"
        : `质量策略已保存，受影响条目已排入维护任务（约 ${scope?.estimated_entries ?? 0} 条）`,
    );
  }
}

// ---- M7: work groups and batches ----
const workTitle = ref("");
const workGroups = ref<Record<string, unknown>[]>([]);
const selectedWorkGroup = ref<string | null>(null);
const workMembers = ref<Record<string, unknown>[]>([]);
const workEntryIds = ref("");
const workBatch = ref<Record<string, unknown> | null>(null);
const batchTag = ref("");

async function createWork() {
  if (!workTitle.value.trim()) return;
  const result = await run(() => api.workGroupCreate(workTitle.value.trim()));
  if (result) message.success("作品组已建立；只建立关系，不合并正文");
  await loadWorks();
}
async function loadWorks() {
  const result = await run(() => api.workGroupList());
  if (result) workGroups.value = result.items as unknown as Record<string, unknown>[];
}
async function openWork(group?: Record<string, unknown>) {
  const id = String(group?.group_id ?? selectedWorkGroup.value ?? "");
  if (!id) return;
  selectedWorkGroup.value = id;
  const result = await run(() => api.workGroupGet(id));
  if (result) workMembers.value = result.members as unknown as Record<string, unknown>[];
}
async function addMembers() {
  if (!selectedWorkGroup.value || !workEntryIds.value.trim()) return;
  const ids = workEntryIds.value.split(/[\s,]+/).filter(Boolean);
  const result = await run(() => api.workGroupAdd(selectedWorkGroup.value!, ids));
  if (result) message.success(`已加入 ${result.added} 条`);
  await openWork();
}
async function previewBatch() {
  const ids = workEntryIds.value.split(/[\s,]+/).filter(Boolean);
  if (ids.length === 0) return;
  const actions = batchTag.value.trim()
    ? { tags_add: [batchTag.value.trim()] }
    : { reevaluate_quality: true };
  const result = await run(() => api.workBatchPreview(ids, actions));
  if (result) workBatch.value = result as unknown as Record<string, unknown>;
}
async function applyBatch() {
  const token = String(workBatch.value?.token ?? "");
  if (!token) return;
  const result = await run(() => api.workBatchApply(token));
  if (result) message.success(`批量完成：${result.ok ? "全部成功" : "部分失败，可逐条重试"}`);
  workBatch.value = null;
}
async function removeMember(member: Record<string, unknown>) {
  if (!selectedWorkGroup.value) return;
  const result = await run(() =>
    api.workGroupRemove(selectedWorkGroup.value!, [String(member.entry_id)]),
  );
  if (result) message.success("已移出作品组；条目本身没有被删除");
  await openWork();
}

// ---- M8/M9: plugins, capabilities and the tool update ----
const pluginPath = ref("");
const releases = ref<Record<string, unknown>[]>([]);
const capabilityContracts = ref<Record<string, unknown>[]>([]);
const activeCapabilities = ref<Record<string, unknown>[]>([]);
const tool = ref<Record<string, unknown> | null>(null);
const toolCheck = ref<Record<string, unknown> | null>(null);

async function loadPlugins() {
  const [list, found, caps, status] = await Promise.all([
    api.pluginList(),
    api.pluginReleases(),
    api.pluginCapabilities(),
    api.toolStatus(),
  ]);
  plugins.value = list.items;
  releases.value = found.items as unknown as Record<string, unknown>[];
  capabilityContracts.value = caps.contracts as unknown as Record<string, unknown>[];
  activeCapabilities.value = caps.active as Record<string, unknown>[];
  tool.value = status as unknown as Record<string, unknown>;
}
async function installPlugin() {
  if (!pluginPath.value.trim()) return;
  const result = await run(() => api.pluginInstall(pluginPath.value.trim()));
  if (result) message.success("已安装；安装只校验与复制，不执行");
  await loadPlugins();
}
async function activateRelease(release: Record<string, unknown>) {
  const result = await run(() =>
    api.pluginActivate(String(release.plugin_id), String(release.version)),
  );
  if (result) message.success("已切换；上一版本保留可回退");
  await loadPlugins();
}
async function rollbackPlugin(release: Record<string, unknown>) {
  const result = await run(() => api.pluginRollback(String(release.plugin_id)));
  if (result) message.success("已回退到上一兼容版本");
  await loadPlugins();
}
async function removeRelease(release: Record<string, unknown>) {
  const result = await run(() =>
    api.pluginRemove(String(release.plugin_id), String(release.version)),
  );
  if (result) message.success("已移除插件文件；档案内容仍可读取");
  await loadPlugins();
}
async function setCapability(plugin: Record<string, unknown>, kind: string, enabled: boolean) {
  const result = await run(() => api.pluginCapabilitySet(String(plugin.plugin_id), kind, enabled));
  if (result) message.success(enabled ? "扩展已启用" : "扩展已关闭；基础归档不受影响");
  await loadPlugins();
}
async function checkTool() {
  const result = await run(() => api.toolCheckUpdate());
  if (result) toolCheck.value = result as unknown as Record<string, unknown>;
}
async function applyTool() {
  const version = String(toolCheck.value?.latest ?? "");
  if (!version) return;
  const result = await run(() => api.toolApplyUpdate(version));
  if (result) {
    message.success("已验签、验摘要、启动检查并切换；上一版本可回退");
    toolCheck.value = null;
  }
  await loadPlugins();
}
async function rollbackTool() {
  const result = await run(() => api.toolRollback());
  if (result) message.success("已回到上一版本");
  await loadPlugins();
}

const creatorColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "类型", key: "kind", width: 150 },
  { title: "源", key: "source_creator_id", width: 180 },
  { title: "目标", key: "target_creator_id", width: 180 },
  {
    title: "账号",
    key: "account_ids",
    render: (row) => String((row.account_ids as unknown[])?.length ?? 0),
  },
  { title: "时间", key: "created_at", width: 190 },
];
const candidateColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "账号", key: "account_id", width: 180 },
  { title: "建议创作者", key: "proposed_creator_id", width: 180 },
  { title: "依据", key: "kind", width: 130 },
  { title: "分数", key: "score", width: 80 },
  {
    title: "",
    key: "actions",
    width: 160,
    render: (row) =>
      h("div", { class: "row-actions" }, [
        h(
          NButton,
          { size: "small", type: "primary", onClick: () => decide(row, true) },
          { default: () => "确认" },
        ),
        h(NButton, { size: "small", onClick: () => decide(row, false) }, { default: () => "拒绝" }),
      ]),
  },
];
const importColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "文件", key: "rel_path" },
  { title: "大小", key: "size", width: 100 },
  { title: "状态", key: "state", width: 110 },
  { title: "说明", key: "error", width: 220, render: (row) => String(row.error ?? "") },
];
const duplicateColumns: DataTableColumns<Record<string, unknown>> = [
  {
    title: "摘要",
    key: "sha256",
    width: 220,
    render: (row) => String(row.sha256 ?? "").slice(0, 16) + "…",
  },
  { title: "大小", key: "size", width: 100 },
  {
    title: "引用条目",
    key: "entries",
    render: (row) => String((row.entries as unknown[])?.length ?? 0),
  },
];
const replayCandidateColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "条目", key: "entry_id", width: 180 },
  { title: "标题", key: "title" },
  { title: "时间", key: "published_at", width: 190 },
  {
    title: "覆盖",
    key: "coverage",
    width: 200,
    render: (row) => coverageText(row.coverage),
  },
  {
    title: "录制独有",
    key: "unique_recorded_ranges",
    width: 190,
    render: (row) =>
      rangesText((row.coverage as Record<string, unknown> | undefined)?.unique_recorded_ranges),
  },
  { title: "质量", key: "quality", width: 120, render: (row) => qualityText(row.quality) },
  {
    title: "不可比较原因",
    key: "comparison_reason",
    render: (row) => comparisonReason(row),
  },
  {
    title: "",
    key: "actions",
    width: 120,
    render: (row) =>
      h(
        NButton,
        { size: "small", onClick: () => selectReplay(row) },
        { default: () => "查看比较" },
      ),
  },
];
const replayColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "条目", key: "entry_id", width: 180 },
  { title: "标题", key: "title" },
  { title: "状态", key: "state", width: 100 },
  { title: "决定", key: "decision", width: 120 },
  {
    title: "覆盖",
    key: "coverage",
    width: 210,
    render: (row) => coverageText(row.coverage),
  },
  {
    title: "录制区间",
    key: "recorded_ranges",
    width: 220,
    render: (row) =>
      rangesText((row.coverage as Record<string, unknown> | undefined)?.recorded_ranges),
  },
  {
    title: "录制独有",
    key: "unique_recorded_ranges",
    width: 190,
    render: (row) =>
      rangesText((row.coverage as Record<string, unknown> | undefined)?.unique_recorded_ranges),
  },
  { title: "质量", key: "quality", width: 120, render: (row) => qualityText(row.quality) },
  {
    title: "不可比较原因",
    key: "reason",
    render: (row) => comparisonReason(row),
  },
  {
    title: "",
    key: "actions",
    width: 200,
    render: (row) =>
      row.state === "candidate"
        ? h("div", { class: "row-actions" }, [
            h(
              NButton,
              { size: "small", type: "primary", onClick: () => linkReplay(row) },
              { default: () => "关联保留两者" },
            ),
            h(
              NButton,
              { size: "small", onClick: () => rejectReplay(row) },
              { default: () => "拒绝" },
            ),
          ])
        : null,
  },
];
const qualityColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "条目", key: "entry_id" },
  { title: "状态", key: "state", width: 130 },
  {
    title: "全部状态",
    key: "states",
    width: 200,
    render: (row) => (row.states as string[] | undefined)?.join("、") ?? "",
  },
  {
    title: "原因",
    key: "reasons",
    render: (row) => (row.reasons as string[] | undefined)?.join("；") ?? "",
  },
  {
    title: "",
    key: "actions",
    width: 220,
    render: (row) =>
      h("div", { class: "row-actions" }, [
        h(
          NButton,
          { size: "small", type: "primary", onClick: () => pinQuality(row, true) },
          { default: () => "固定" },
        ),
        h(
          NButton,
          { size: "small", onClick: () => pinQuality(row, false) },
          { default: () => "解除" },
        ),
        h(
          NButton,
          { size: "small", onClick: () => reevaluate(row) },
          { default: () => "重新评估" },
        ),
      ]),
  },
];
const workColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "作品组", key: "title" },
  { title: "条目", key: "members", width: 100 },
  { title: "说明", key: "description" },
  {
    title: "",
    key: "actions",
    width: 100,
    render: (row) =>
      h(NButton, { size: "small", onClick: () => openWork(row) }, { default: () => "查看" }),
  },
];
const releaseColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "插件", key: "plugin_id", width: 150 },
  { title: "版本", key: "version", width: 100 },
  { title: "状态", key: "state", width: 100 },
  { title: "协议", key: "protocol", width: 80 },
  {
    title: "",
    key: "actions",
    width: 260,
    render: (row) =>
      h("div", { class: "row-actions" }, [
        h(
          NButton,
          { size: "small", onClick: () => activateRelease(row) },
          { default: () => "启用" },
        ),
        h(
          NButton,
          { size: "small", onClick: () => rollbackPlugin(row) },
          { default: () => "回退" },
        ),
        h(NButton, { size: "small", onClick: () => removeRelease(row) }, { default: () => "移除" }),
      ]),
  },
];

onMounted(async () => {
  const requested = String(route.query.tab ?? "");
  if (requested) tab.value = requested;
  try {
    await loadCreators();
    await refreshIdentity();
    await refreshImports();
    await loadPlugins();
  } catch (error) {
    fail(error);
  }
});
</script>

<template>
  <n-card title="管理与扩展" data-page="management">
    <p class="hint">
      这里的操作都显式执行并留下记录：不会自动合并、删除或回收，原地索引的外部文件不会被移动，相似候选和重复只供判断，固定保留的版本优先于自动回收，移除插件不删除档案。
    </p>
    <n-tabs v-model:value="tab" type="line" animated>
      <n-tab-pane name="identity" tab="身份关系">
        <div class="grid">
          <n-select
            v-model:value="sourceCreator"
            :options="creatorOptions"
            placeholder="源创作者"
            clearable
          />
          <n-select
            v-model:value="targetCreator"
            :options="creatorOptions"
            placeholder="目标创作者"
            clearable
          />
          <n-button :loading="busy" @click="previewMerge">预览合并影响</n-button>
          <n-button type="primary" :loading="busy" :disabled="!mergePreview" @click="merge"
            >按预览确认合并</n-button
          >
        </div>
        <pre v-if="mergePreview" class="json">{{
          JSON.stringify(mergePreview.impact, null, 2)
        }}</pre>
        <div class="grid">
          <n-input v-model:value="splitAccounts" placeholder="要拆出的账号 ID，用逗号分隔" />
          <n-button :loading="busy" @click="previewSplit">预览拆分影响</n-button>
          <n-button :loading="busy" :disabled="!splitPreview" @click="split"
            >按预览确认拆分</n-button
          >
        </div>
        <pre v-if="splitPreview" class="json">{{
          JSON.stringify(splitPreview.impact, null, 2)
        }}</pre>
        <div class="grid">
          <n-input v-model:value="relinkAccount" placeholder="账号 ID" />
          <n-select
            v-model:value="relinkExpected"
            :options="creatorOptions"
            placeholder="当前创作者（预期）"
            clearable
          />
          <n-select
            v-model:value="relinkTarget"
            :options="creatorOptions"
            placeholder="目标创作者"
            clearable
          />
          <n-button :loading="busy" @click="previewRelink">预览关联影响</n-button>
          <n-button :loading="busy" :disabled="!relinkPreview" @click="relink"
            >按预览确认关联</n-button
          >
        </div>
        <pre v-if="relinkPreview" class="json">{{
          JSON.stringify(relinkPreview.impact, null, 2)
        }}</pre>
        <h4>待确认的身份候选</h4>
        <n-data-table
          :columns="candidateColumns"
          :data="candidates"
          size="small"
          :bordered="false"
        />
        <p v-if="candidates.length === 0" class="empty">
          没有待确认候选；相似昵称只产生候选，不自动绑定。
        </p>
        <h4>操作记录</h4>
        <n-data-table :columns="creatorColumns" :data="operations" size="small" :bordered="false" />
        <h4>资料观察历史</h4>
        <div class="grid">
          <n-input v-model:value="historyAccount" placeholder="账号 ID" />
          <n-button :loading="busy" @click="loadHistory">查看历史</n-button>
        </div>
        <n-data-table
          :columns="[
            { title: '序号', key: 'sequence', width: 70 },
            { title: '昵称', key: 'display_name', width: 160 },
            { title: '观察时间', key: 'observed_at', width: 200 },
            { title: '资料', key: 'profile', render: (row) => JSON.stringify(row.profile) },
          ]"
          :data="history"
          size="small"
          :bordered="false"
        />
      </n-tab-pane>

      <n-tab-pane name="imports" tab="本地导入">
        <div class="grid">
          <n-input v-model:value="importPath" placeholder="本机文件或目录的完整路径" />
          <n-select
            v-model:value="importMode"
            :options="[
              { label: '托管复制（复制进对象库）', value: 'managed' },
              { label: '原地索引（不移动外部文件）', value: 'in_place' },
            ]"
          />
          <n-button :loading="busy" @click="inspectImport">预览</n-button>
          <n-button
            type="primary"
            :loading="busy"
            :disabled="!importInspect || !importConfirm"
            @click="startImport"
            >开始导入</n-button
          >
        </div>
        <p v-if="importInspect" class="hint">
          预览：{{ importInspect.files }} 个文件、{{ importInspect.bytes }} 字节；不可读
          {{ importInspect.unreadable }} 项；重复候选
          {{ (importInspect.duplicates as unknown[])?.length ?? 0 }} 项；链路跳过
          {{ importInspect.links_skipped }} 项。预览不复制、不建条目。
        </p>
        <n-checkbox v-if="importInspect" v-model:checked="importConfirm">
          我已核对当前路径、导入方式和目标创作者，确认按此预览开始
        </n-checkbox>
        <div v-if="importBatch" class="grid">
          <n-tag>{{ String(importBatch.state) }}</n-tag>
          <n-tag>已完成 {{ importBatch.done_items }} / {{ importBatch.total_items }}</n-tag>
          <n-tag type="warning">失败 {{ importBatch.failed_items }}</n-tag>
          <n-button size="small" :loading="busy" @click="importAction('check')"
            >检查外部文件</n-button
          >
          <n-button size="small" :loading="busy" @click="importAction('cancel')">取消</n-button>
          <n-button size="small" :loading="busy" @click="importAction('resume')"
            >恢复未完成项</n-button
          >
        </div>
        <n-data-table :columns="importColumns" :data="importItems" size="small" :bordered="false" />
        <div class="grid">
          <n-select
            v-model:value="assignCreator"
            :options="creatorOptions"
            placeholder="归入创作者"
            clearable
          />
          <n-button :loading="busy" @click="importAction('assign')">整理到创作者</n-button>
        </div>
        <h4>最近的导入批次</h4>
        <n-data-table
          :columns="[
            { title: '批次', key: 'batch_id', width: 200 },
            { title: '方式', key: 'mode', width: 100 },
            { title: '状态', key: 'state', width: 100 },
            { title: '文件', key: 'total_items', width: 80 },
          ]"
          :data="importBatches"
          size="small"
          :bordered="false"
        />
      </n-tab-pane>

      <n-tab-pane name="compare" tab="重复与比较">
        <div class="grid">
          <n-button :loading="busy" @click="loadDuplicates">按摘要查看精确重复</n-button>
        </div>
        <n-data-table
          :columns="duplicateColumns"
          :data="duplicates"
          size="small"
          :bordered="false"
        />
        <div class="grid">
          <n-input v-model:value="similarEntry" placeholder="条目 ID（相似候选）" />
          <n-button :loading="busy" @click="findSimilar">查找相似</n-button>
        </div>
        <p v-if="similarNote" class="empty">{{ similarNote }}</p>
        <n-data-table
          :columns="[
            { title: '条目', key: 'entry_id' },
            { title: '媒体', key: 'media_id' },
            { title: '距离', key: 'distance', width: 100 },
          ]"
          :data="similar"
          size="small"
          :bordered="false"
        />
        <h4>并排比较（按条目）</h4>
        <div class="grid">
          <n-input v-model:value="leftId" placeholder="左侧条目 ID" />
          <n-input v-model:value="rightId" placeholder="右侧条目 ID" />
          <n-button :loading="busy" @click="compare">比较</n-button>
        </div>
        <pre v-if="comparison" class="json">{{ JSON.stringify(comparison, null, 2) }}</pre>
        <div class="grid">
          <n-button
            v-for="option in decisionOptions"
            :key="option.value"
            size="small"
            :loading="busy"
            @click="
              decideComparison(option.value as 'not_duplicate' | 'keep_both' | 'different_versions')
            "
          >
            {{ option.label }}
          </n-button>
        </div>
        <p class="hint">决定只记录，不删除、不合并、不替换；删除仍走现有的显式删除流程。</p>
      </n-tab-pane>

      <n-tab-pane name="replays" tab="回放关联">
        <div class="grid">
          <n-input v-model:value="replaySession" placeholder="录制场次 ID" />
          <n-button :loading="busy" @click="loadReplays">查候选</n-button>
        </div>
        <n-data-table
          :columns="replayCandidateColumns"
          :data="replayCandidates"
          size="small"
          :bordered="false"
        />
        <n-card v-if="replayChoice" size="small" title="关联前比较（只读）" class="replay-compare">
          <p>
            覆盖：{{ coverageText(replayChoice.coverage) }}；录制区间：{{
              replayCoverageRanges(replayChoice.coverage, "recorded_ranges")
            }}；录制独有：{{
              replayCoverageRanges(replayChoice.coverage, "unique_recorded_ranges")
            }}
          </p>
          <p>
            质量：{{ qualityText(replayChoice.quality) }}；{{
              comparisonReason(replayChoice) || "主版本条件已满足"
            }}
          </p>
          <div class="grid">
            <n-button type="primary" :loading="busy" @click="linkSelectedReplay('keep_both')"
              >关联并保留两者</n-button
            >
            <n-button
              :loading="busy"
              :disabled="!replayChoice.can_replay_primary"
              @click="linkSelectedReplay('replay_primary')"
              >设回放为主</n-button
            >
            <n-button :loading="busy" @click="rejectSelectedReplay">拒绝关联</n-button>
            <n-button :loading="busy" @click="clearReplayChoice">取消</n-button>
          </div>
        </n-card>
        <p v-if="replayChoice && !replayChoice.can_replay_primary" class="hint">
          只有回放完整覆盖录制且质量可比较时才可设回放为主；现在只能保留两者或拒绝。
        </p>
        <h4>已建立的关联</h4>
        <n-data-table :columns="replayColumns" :data="replayLinks" size="small" :bordered="false" />
        <p class="hint">
          回放缺少的录制区间会列在关联记录里；录制独有内容始终保留，不自动替换或回收。
        </p>
      </n-tab-pane>

      <n-tab-pane name="quality" tab="质量筛选">
        <div class="grid">
          <n-select
            v-model:value="qualityState"
            :options="qualityStates"
            placeholder="全部状态"
            clearable
          />
          <n-button :loading="busy" @click="loadQuality">筛选</n-button>
          <n-switch v-model:value="qualityPreferFps" />
          <span>优先高帧率</span>
          <n-input-number v-model:value="qualityWait" :min="0" :max="2592000" />
          <span>回收等待秒数</span>
          <n-button :loading="busy" @click="saveQuality()">保存并重新评估</n-button>
        </div>
        <n-data-table
          :columns="qualityColumns"
          :data="qualityItems"
          size="small"
          :bordered="false"
        />
        <p class="hint">固定保留的版本不会被自动回收；不可比较不会被标成更低质量。</p>
      </n-tab-pane>

      <n-tab-pane name="works" tab="作品聚合">
        <div class="grid">
          <n-input v-model:value="workTitle" placeholder="作品组标题" />
          <n-button :loading="busy" @click="createWork">新建作品组</n-button>
        </div>
        <n-data-table :columns="workColumns" :data="workGroups" size="small" :bordered="false" />
        <div class="grid">
          <n-input v-model:value="workEntryIds" placeholder="条目 ID，用逗号分隔" />
          <n-button :loading="busy" @click="addMembers">加入当前组</n-button>
        </div>
        <n-data-table
          :columns="[
            { title: '条目', key: 'entry_id', width: 180 },
            { title: '平台', key: 'namespace', width: 100 },
            { title: '标题', key: 'title' },
            {
              title: '',
              key: 'actions',
              width: 90,
              render: (row) =>
                h(
                  NButton,
                  { size: 'small', onClick: () => removeMember(row) },
                  { default: () => '移出' },
                ),
            },
          ]"
          :data="workMembers"
          size="small"
          :bordered="false"
        />
        <div class="grid">
          <n-input v-model:value="batchTag" placeholder="批量添加的标签（留空则重新评估质量）" />
          <n-button :loading="busy" @click="previewBatch">预览影响</n-button>
          <n-button type="primary" :loading="busy" :disabled="!workBatch" @click="applyBatch"
            >按预览执行</n-button
          >
        </div>
        <pre v-if="workBatch" class="json">{{ JSON.stringify(workBatch, null, 2) }}</pre>
        <p class="hint">
          批量动作只接受标签、作品组、同步未下载媒体和重新评估质量；预览令牌过期或对象变化会拒绝执行。
        </p>
      </n-tab-pane>

      <n-tab-pane name="plugins" tab="插件与能力">
        <div class="grid">
          <n-input v-model:value="pluginPath" placeholder="本机插件目录（含 plugin.json）" />
          <n-button :loading="busy" @click="installPlugin">安装</n-button>
          <n-button :loading="busy" @click="loadPlugins">刷新</n-button>
        </div>
        <n-data-table :columns="releaseColumns" :data="releases" size="small" :bordered="false" />
        <h4>已安装模块</h4>
        <n-data-table
          :columns="[
            { title: '模块', key: 'plugin_id' },
            { title: '平台', key: 'platform', width: 110 },
            { title: '状态', key: 'state', width: 100 },
            {
              title: '能力',
              key: 'capabilities',
              render: (row) => (row.capabilities as string[])?.join('、'),
            },
          ]"
          :data="plugins"
          size="small"
          :bordered="false"
        />
        <h4>可选扩展</h4>
        <div
          v-for="plugin in activeCapabilities"
          :key="String(plugin.plugin_id)"
          class="capability"
        >
          <strong>{{ plugin.plugin_id }}</strong>
          <n-tag
            v-for="cap in plugin.capabilities as Record<string, unknown>[]"
            :key="String(cap.kind)"
            size="small"
          >
            {{ cap.kind }} · {{ cap.enabled ? "开" : "关" }}
            <n-switch
              :value="Boolean(cap.enabled)"
              size="small"
              @update:value="(value: boolean) => setCapability(plugin, String(cap.kind), value)"
            />
          </n-tag>
        </div>
        <p class="hint">
          关闭可选扩展不影响基础采集、时间线和导出；适配器始终通过 Core 的代理，不接触 Cookie。
        </p>
      </n-tab-pane>

      <n-tab-pane name="tool" tab="外部工具">
        <div v-if="tool" class="grid">
          <n-tag>锁定 {{ tool.locked }}</n-tag>
          <n-tag :type="tool.ready ? 'success' : 'warning'"
            >当前 {{ tool.current ?? "不可用" }}</n-tag
          >
          <n-tag>来源 {{ tool.source }}</n-tag>
          <n-tag v-if="tool.error">{{ tool.error }}</n-tag>
        </div>
        <div class="grid">
          <n-button :loading="busy" @click="checkTool">检查更新</n-button>
          <n-button type="primary" :loading="busy" :disabled="!toolCheck?.newer" @click="applyTool">
            安装 {{ toolCheck?.latest ?? "" }}
          </n-button>
          <n-button :loading="busy" @click="rollbackTool">回退</n-button>
        </div>
        <p v-if="toolCheck" class="hint">
          最新 {{ toolCheck.latest }}，当前 {{ toolCheck.current }}，{{
            toolCheck.newer ? "有新版本" : "已是最新"
          }}；检查时间 {{ toolCheck.checked_at }}。
        </p>
        <p class="hint">
          yt-dlp 只随包锁定版本；更新只由这里点击触发，先核对发布签名再用 SHA2-256SUMS
          核对摘要，并排安装、启动检查、空闲切换，保留上一版回退，不使用 -U。
        </p>
      </n-tab-pane>
    </n-tabs>
  </n-card>
</template>

<style scoped>
.hint,
.empty {
  margin: 10px 0;
  color: #888;
  font-size: 0.75rem;
}
.grid {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin: 10px 0;
}
.grid > :deep(.n-input),
.grid > :deep(.n-select) {
  min-width: 220px;
  flex: 1;
}
.row-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.capability {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin: 6px 0;
}
.json {
  max-height: 260px;
  overflow: auto;
  padding: 10px;
  border-radius: 6px;
  background: rgba(0, 0, 0, 0.04);
  font-size: 0.75rem;
}
</style>
