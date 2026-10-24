<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { isTauri } from "@tauri-apps/api/core";
import { useMessage } from "naive-ui";
import { useWorkspaceStore } from "../stores/workspace";
import { filterFeed, previewEntries, type FeedEntry } from "../lib/preview";
import { SORT_OPTIONS, SYNC_STATE_LABELS } from "../lib/sites";
import {
  api,
  type AccountDto,
  type AuthenticationState,
  type CreatorDto,
  type SubscriptionDto,
} from "../lib/api";
import CreatorBanner from "./home/CreatorBanner.vue";
import EntryDetail from "./home/EntryDetail.vue";
import FeedFilters from "./home/FeedFilters.vue";
import FeedTimeline from "./home/FeedTimeline.vue";
import { localDayEnd, mapEntry, type LiveEntry } from "./home/entries";
import { useEntryDetail } from "./home/useEntryDetail";
import "../styles/home.css";

const workspace = useWorkspaceStore();
const message = useMessage();
const route = useRoute(),
  router = useRouter();
const focusedEntry = computed(() =>
  typeof route.query.entry === "string" ? route.query.entry : undefined,
);
const kind = ref("");
const authentication = ref<AuthenticationState | "">("");
const oldest = ref(false);
const sort = ref(""),
  tag = ref(""),
  contentKind = ref("");
const tagOptions = ref<string[]>([]);
const fromDate = ref(""),
  beforeDate = ref(""),
  minHeight = ref(""),
  codec = ref(""),
  saveState = ref("");
const selectedId = ref<string | null>(workspace.preview ? "preview-1" : null);
const relatedTab = ref(false);
const subscribed = ref(true);
const { detail, loadDetail, moreMedia } = useEntryDetail(selectedId);
const loading = ref(false);
const error = ref("");
const liveEntries = ref<LiveEntry[]>([]);
const selectionNotice = ref("");
let loadedPages = 0;
let requestedPages = 1;
let appliedScope = "";
const hasMore = ref(false);
const nextCursor = ref<string | null>(null);
const counts = ref<{ global: number | null; filtered: number | null }>({
  global: null,
  filtered: null,
});
const creators = ref<CreatorDto[]>([]);
const accounts = ref<AccountDto[]>([]);
const subscriptions = ref<SubscriptionDto[]>([]);
const creatorId = ref("");
const accountId = ref("");
const managementError = ref("");
const savingSubscription = ref(false);
const visibleAccounts = computed(() =>
  accounts.value.filter((a) => !creatorId.value || a.creator_id === creatorId.value),
);
const creator = computed(() => creators.value.find((c) => c.creator_id === creatorId.value));
const subscription = computed(() =>
  subscriptions.value.find((s) => s.account_id === accountId.value),
);
let alive = true;
let requestId = 0;
let searchTimer: ReturnType<typeof setTimeout> | undefined;
let changeTimer: ReturnType<typeof setTimeout> | undefined;
const unlisten: Array<() => void> = [];
async function connectEvents() {
  if (workspace.preview || !isTauri()) return;
  const { listen } = await import("@tauri-apps/api/event");
  const refresh = () => {
    clearTimeout(changeTimer);
    changeTimer = setTimeout(() => {
      if (alive) {
        void load(false, true);
        void loadManagement();
      }
    }, 250);
  };
  for (const [event, handler] of [
    ["trace://change", refresh],
    ["trace://resync", refresh],
    [
      "trace://disconnected",
      () => {
        if (alive) error.value = "后台连接已断开，重新连接后会刷新档案。";
      },
    ],
  ] as const) {
    const stop = await listen(event, handler);
    if (alive) unlisten.push(stop);
    else stop();
  }
}
const entries = computed(() => (workspace.preview ? previewEntries : liveEntries.value));
const filtered = computed(() =>
  workspace.preview
    ? filterFeed(entries.value, workspace.query, kind.value, workspace.platform, oldest.value)
    : liveEntries.value,
);
const groups = computed(() =>
  sort.value && !workspace.preview
    ? [
        {
          day: `${SORT_OPTIONS.find((o) => o.value === sort.value)?.label ?? sort.value}（${oldest.value ? "从低到高" : "从高到低"}）`,
          items: filtered.value,
        },
      ]
    : Array.from(new Set(filtered.value.map((x) => x.day))).map((day) => ({
        day,
        items: filtered.value.filter((x) => x.day === day),
      })),
);
const selected = computed(() => filtered.value.find((x) => x.id === selectedId.value) ?? null);
const related = computed(() => entries.value.filter((x) => x.id !== selectedId.value).slice(0, 3));
function resetFilters() {
  if (focusedEntry.value) void router.replace({ path: "/", query: {} });
  workspace.query = "";
  workspace.platform = "";
  kind.value = "";
  fromDate.value = "";
  beforeDate.value = "";
  minHeight.value = "";
  codec.value = "";
  saveState.value = "";
  authentication.value = "";
  sort.value = "";
  tag.value = "";
  contentKind.value = "";
}
function previewAction() {
  message.info("这是界面预览，示例内容不会创建下载任务或写入档案。");
}
function toggleSubscription() {
  subscribed.value = !subscribed.value;
  message.info(subscribed.value ? "预览：已订阅（未写入档案）" : "预览：已暂停订阅（未写入档案）");
}
async function openSource() {
  if (workspace.preview) {
    previewAction();
    return;
  }
  if (!selected.value?.sourceUrl) return;
  try {
    await api.openSource(selected.value.id);
  } catch {
    message.error("无法打开来源，请检查后台连接。");
  }
}
function selectEntry(entry: FeedEntry) {
  selectedId.value = entry.id;
  relatedTab.value = false;
  selectionNotice.value = "";
}
function pickRelated(entry: FeedEntry) {
  resetFilters();
  selectEntry(entry);
}
async function load(append = false, refresh = false) {
  if (workspace.preview) return;
  const current = ++requestId;
  const options = {
    authentication: authentication.value || undefined,
    entryId: focusedEntry.value,
    limit: 50,
    creatorId: creatorId.value || undefined,
    accountId: accountId.value || undefined,
    query: workspace.query.trim() || undefined,
    namespace: workspace.platform || undefined,
    kind: kind.value || undefined,
    oldest: oldest.value,
    publishedFrom: fromDate.value
      ? new Date(fromDate.value + "T00:00:00").toISOString()
      : undefined,
    publishedBefore: beforeDate.value ? localDayEnd(beforeDate.value) : undefined,
    minHeight: minHeight.value ? Number(minHeight.value) : undefined,
    codec: codec.value || undefined,
    saveState: saveState.value || undefined,
    sort: sort.value || undefined,
    tag: tag.value.trim() || undefined,
    contentKind: contentKind.value || undefined,
  };
  const scope = JSON.stringify(options);
  const sameScope = appliedScope === scope;
  append = append && sameScope;
  const boundary = refresh && sameScope ? liveEntries.value.at(-1) : undefined;
  const targetPages = append ? loadedPages + 1 : refresh && sameScope ? requestedPages : 1;
  requestedPages = targetPages;
  loading.value = true;
  error.value = "";
  try {
    let cursor = append ? nextCursor.value : null;
    let pages = append ? loadedPages : 0;
    let mapped = append ? [...liveEntries.value] : [];
    let latestCounts: typeof counts.value = { global: null, filtered: null };
    do {
      const result = await api.archiveTimeline({ ...options, cursor: cursor ?? undefined });
      if (current !== requestId || !alive) return;
      mapped.push(...result.items.map(mapEntry));
      pages++;
      if (cursor && result.next_cursor === cursor) throw new Error("non advancing cursor");
      cursor = result.next_cursor;
      latestCounts = result.counts ?? { global: null, filtered: null };
      const tail = mapped.at(-1);
      // Follow the previous window's sort boundary even when new entries push it to another page.
      // A removed boundary still has a comparable time/id, so it does not force an archive-wide scan.
      const reachedBoundary =
        !boundary ||
        !tail ||
        (() => {
          const rank = Number(tail.publishedAt === null) - Number(boundary.publishedAt === null);
          if (rank) return rank > 0;
          const a = tail.publishedAt ?? "",
            b = boundary.publishedAt ?? "";
          const order =
            a < b ? -1 : a > b ? 1 : tail.id < boundary.id ? -1 : tail.id > boundary.id ? 1 : 0;
          return options.oldest ? order >= 0 : order <= 0;
        })();
      if (append || (pages >= targetPages && reachedBoundary)) break;
    } while (cursor);
    if (current !== requestId || !alive) return;
    mapped = [...new Map(mapped.map((entry) => [entry.id, entry])).values()];
    const previous = selectedId.value;
    if (refresh && sameScope && previous && !mapped.some((entry) => entry.id === previous)) {
      const membership = await api.archiveTimeline({ ...options, entryId: previous, limit: 1 });
      if (current !== requestId || !alive) return;
      if (selectedId.value !== previous) {
        void load(false, true);
        return;
      }
      const stillMatches = membership.items.find((entry) => entry.entry_id === previous);
      if (stillMatches) mapped.push(mapEntry(stillMatches));
    }
    const retained = previous !== null && mapped.some((entry) => entry.id === previous);
    // Publish a coherent replacement without transiently emptying the selected detail/player.
    liveEntries.value = mapped;
    nextCursor.value = cursor;
    hasMore.value = Boolean(cursor);
    counts.value = latestCounts;
    loadedPages = pages;
    requestedPages = pages;
    appliedScope = scope;
    if (previous && !retained) selectionNotice.value = "原选择已不在当前结果中，已切换到可用内容。";
    if (refresh && retained) void loadDetail(previous, true);
  } catch {
    if (current === requestId)
      error.value =
        "暂时无法读取档案。请检查后台连接；搜索索引清理或预算不足时，请在存储页重建或调整预算。";
  } finally {
    if (current === requestId) loading.value = false;
  }
}
async function loadManagement() {
  if (workspace.preview) return;
  try {
    const [c, a, s] = await Promise.all([
      api.creatorList(),
      api.accountList(),
      api.subscriptionList(),
    ]);
    if (!alive) return;
    creators.value = c.items;
    accounts.value = a.items;
    subscriptions.value = s.items;
    managementError.value = "";
  } catch {
    if (alive) managementError.value = "创作者或订阅读取失败，请重试。";
  }
}
async function updateSubscription() {
  if (workspace.preview || !accountId.value) return;
  savingSubscription.value = true;
  try {
    if (subscription.value)
      await api.subscriptionSetEnabled(
        subscription.value.subscription_id,
        subscription.value.state !== "enabled",
      );
    else await api.subscriptionCreate(accountId.value, "from_now");
    await loadManagement();
    if (alive) message.success("订阅设置已保存。");
  } catch {
    if (alive) message.error("订阅未能保存，请检查后台后重试。");
  } finally {
    savingSubscription.value = false;
  }
}
watch(filtered, (list) => {
  if (!list.some((x) => x.id === selectedId.value)) selectedId.value = list[0]?.id ?? null;
});
watch(
  focusedEntry,
  (id) => {
    if (id) {
      workspace.query = "";
      workspace.platform = "";
      kind.value = "";
      creatorId.value = "";
      accountId.value = "";
      fromDate.value = "";
      beforeDate.value = "";
      minHeight.value = "";
      codec.value = "";
      saveState.value = "";
      sort.value = "";
      tag.value = "";
      contentKind.value = "";
    }
  },
  { immediate: true },
);
watch(selectedId, (id) => {
  void loadDetail(id);
});
watch(
  [
    focusedEntry,
    () => workspace.query,
    () => workspace.platform,
    kind,
    oldest,
    creatorId,
    accountId,
    fromDate,
    beforeDate,
    minHeight,
    codec,
    saveState,
    authentication,
    sort,
    tag,
    contentKind,
  ],
  () => {
    if (workspace.preview) return;
    clearTimeout(searchTimer);
    ++requestId; // A pending response must not overwrite a new search.
    requestedPages = 1;
    searchTimer = setTimeout(() => {
      void load();
    }, 250);
  },
);
watch(creatorId, () => {
  accountId.value = "";
});
async function loadTags() {
  if (workspace.preview) return;
  try {
    const rows = await api.archiveTags({ limit: 40 });
    if (alive) tagOptions.value = rows.map((r) => r.tag);
  } catch {
    /* Suggestions are optional; the tag filter still works by typing. */
  }
}
onMounted(() => {
  void load(false, true);
  void loadManagement();
  void loadTags();
  void connectEvents().catch(() => {
    if (alive) error.value = "后台事件连接失败，请重试或重新打开窗口。";
  });
});
onBeforeUnmount(() => {
  alive = false;
  clearTimeout(searchTimer);
  clearTimeout(changeTimer);
  unlisten.forEach((stop) => stop());
  ++requestId;
});
async function fetchEntry(id: string | null = selectedId.value) {
  if (!id || workspace.preview) return;
  try {
    const report = await api.archiveEntrySync([id]);
    const item = report.items[0];
    const text = SYNC_STATE_LABELS[item?.state ?? "failed"] ?? item?.state ?? "";
    selectionNotice.value = item?.message ? `${text}：${item.message}` : text;
    if (item?.state === "queued") void load(false, true);
  } catch {
    selectionNotice.value = "补下载未能启动，请查看后台状态后重试。";
  }
}
async function recheckEntry() {
  if (!selectedId.value) return;
  try {
    await api.archiveEntryRecheck(selectedId.value);
    selectionNotice.value = "已安排此内容的详情与资源补查。";
  } catch {
    selectionNotice.value = "补查未能启动，请确认来源订阅已启用并查看后台状态。";
  }
}
</script>

<template>
  <div class="home-layout" data-page="home">
    <section class="feed-column" aria-label="创作者时间线">
      <CreatorBanner
        v-model:creatorId="creatorId"
        v-model:accountId="accountId"
        :creator="creator"
        :creators="creators"
        :visible-accounts="visibleAccounts"
        :subscription="subscription"
        :subscribed="subscribed"
        :saving-subscription="savingSubscription"
        :management-error="managementError"
        @retry="loadManagement"
        @toggle="toggleSubscription"
        @save="updateSubscription"
        @preview-action="previewAction"
      />
      <FeedFilters
        v-model:kind="kind"
        v-model:oldest="oldest"
        v-model:authentication="authentication"
        v-model:fromDate="fromDate"
        v-model:beforeDate="beforeDate"
        v-model:minHeight="minHeight"
        v-model:codec="codec"
        v-model:saveState="saveState"
        v-model:sort="sort"
        v-model:tag="tag"
        v-model:contentKind="contentKind"
        :tag-options="tagOptions"
        :counts="counts"
        :shown="filtered.length"
        @reset="resetFilters"
      />
      <FeedTimeline
        :groups="groups"
        :entry-count="entries.length"
        :shown="filtered.length"
        :loading="loading"
        :error="error"
        :has-more="hasMore"
        :focused-entry="focusedEntry"
        :selected-id="selectedId"
        @select="selectEntry"
        @reset="resetFilters"
        @retry="load(false)"
        @fetch="(entry) => fetchEntry(entry.id)"
        @more="load(true)"
        @preview-action="previewAction"
      />
    </section>

    <EntryDetail
      v-model:relatedTab="relatedTab"
      :selected="selected"
      :selected-id="selectedId"
      :related="related"
      :detail="detail"
      :selection-notice="selectionNotice"
      @refresh-live="loadDetail(selectedId, true)"
      @more-media="moreMedia"
      @open-source="openSource"
      @recheck="recheckEntry"
      @fetch="fetchEntry()"
      @pick="pickRelated"
      @preview-action="previewAction"
    />
  </div>
</template>
