<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import {
  NButton,
  NCard,
  NDataTable,
  NInput,
  NModal,
  NSelect,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { h } from "vue";
import MediaBrowser from "../components/MediaBrowser.vue";
import { api, type MediaDto, type TimelineItemDto } from "../lib/api";
import {
  accessStateLabel,
  formatTime,
  mediaSummaryLabel,
  platformOf,
  qualityLabel,
  saveProgressLabel,
} from "../lib/format";

const message = useMessage();
const items = ref<TimelineItemDto[]>([]);
const loading = ref(false);
const query = ref("");
const kindFilter = ref<string | null>(null);
const nextCursor = ref<string | null>(null);
const detail = ref<Record<string, unknown> | null>(null);
const detailMedia = ref<MediaDto[]>([]);
const detailRevisions = ref<Record<string, unknown>[]>([]);

const kindOptions = [
  { label: "全部类型", value: "" },
  { label: "视频", value: "video" },
];

let requestGeneration = 0;
let detailGeneration = 0;
async function load(append: boolean, cursor?: string) {
  const generation = ++requestGeneration;
  loading.value = true;
  try {
    const r = await api.archiveTimeline({query:query.value.trim() || undefined, kind:kindFilter.value || undefined, cursor, limit:50});
    if (generation !== requestGeneration) return;
    items.value = append ? items.value.concat(r.items) : r.items;
    nextCursor.value = r.next_cursor;
  } catch (e) {
    if (generation === requestGeneration) message.error(String(e));
  } finally {
    if (generation === requestGeneration) loading.value = false;
  }
}

async function openDetail(row: TimelineItemDto) {
  const generation = ++detailGeneration;
  try {
    const r = await api.archiveEntryGet(row.entry_id);
    if (generation !== detailGeneration) return;
    detail.value = r.entry as Record<string, unknown>;
    detailRevisions.value = r.revisions as Record<string, unknown>[];
    detailMedia.value = r.media ?? [];
  } catch (e) {
    message.error(String(e));
  }
}

const columns: DataTableColumns<TimelineItemDto> = [
  { title: "标题", key: "title", minWidth: 220 },
  {
    title: "平台",
    key: "namespace",
    width: 100,
    render: (r) => platformOf(r.namespace),
  },
  {
    title: "发布时间",
    key: "published_at",
    width: 170,
    render: (r) => formatTime(r.published_at),
  },
  {
    title: "可访问",
    key: "access_state",
    width: 100,
    render: (r) => accessStateLabel(r.access_state),
  },
  {
    title: "保存进度",
    key: "save_progress",
    width: 110,
    render: (r) => saveProgressLabel(r.save_progress),
  },
  {
    title: "质量",
    key: "quality_state",
    width: 100,
    render: (r) => qualityLabel(r.quality_state),
  },
  {
    title: "媒体",
    key: "media",
    width: 140,
    render: (r) => mediaSummaryLabel(r.media_summary),
  },
  {
    title: "",
    key: "actions",
    width: 90,
    render: (r) =>
      h(NButton, { size: "small", quaternary: true, onClick: () => openDetail(r) }, { default: () => "详情" }),
  },
];

onMounted(() => load(false));
onBeforeUnmount(() => { ++requestGeneration; ++detailGeneration; });
</script>

<template>
  <n-card title="时间线">
    <div class="toolbar">
      <n-input
        v-model:value="query"
        placeholder="中文搜索（≥3 字走全文索引，1-2 字也可）"
        clearable
        @keyup.enter="load(false)"
      />
      <n-select v-model:value="kindFilter" :options="kindOptions" style="width: 140px" />
      <n-button type="primary" :loading="loading" @click="load(false)">
        {{ query.trim() ? "搜索" : "刷新" }}
      </n-button>
    </div>
    <n-data-table :columns="columns" :data="items" :loading="loading" size="small" />
    <template #footer>
      <n-button v-if="nextCursor" size="small" @click="load(true, nextCursor ?? undefined)">
        加载更多
      </n-button>
    </template>

    <n-modal
      :show="detail !== null"
      preset="card"
      title="条目详情"
      style="width: min(720px, 94vw)"
      @update:show="(v: boolean) => { if (!v) detail = null }"
    >
      <n-tag v-if="detail" size="small">{{ String(detail["entry_id"] ?? "") }}</n-tag>
      <h3 v-if="detail">{{ String(detail["title"] ?? "") }}</h3>
      <p v-if="detail" class="body">{{ String(detail["body"] ?? "（无正文）") }}</p>
      <MediaBrowser :media="detailMedia"/>
      <h4 v-if="detailRevisions.length" class="rev-title">修订记录（{{ detailRevisions.length }}）</h4>
      <div v-for="(rev, i) in detailRevisions" :key="i" class="rev">
        <n-tag size="tiny">{{ String(rev["revision_id"] ?? "") }}</n-tag>
        完整度 {{ String(rev["completeness"] ?? "?") }} · 观察 {{ formatTime(String(rev["observed_at"] ?? "")) }}
      </div>
    </n-modal>
  </n-card>
</template>

<style scoped>
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}
.hint {
  color: #888;
  font-size: 12px;
}
.body {
  white-space: pre-wrap;
}
.rev {
  margin: 4px 0;
  font-size: 12px;
}
.rev-title {
  margin: 12px 0 4px;
}
</style>
