<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import type { ExplorerRow, ListView } from "../../lib/api";
import { formatBytes, formatTime } from "../../lib/format";
import { displayText, hasHiddenCharacters } from "../../lib/safe";
import {
  copyStateLabel,
  entryKindLabel,
  formatDuration,
  CONTENT_KIND_OPTIONS,
  siteLabel,
} from "../../lib/sites";

const props = defineProps<{
  list: ListView | null;
  loading: boolean;
  error: string;
  side: "library" | "downloads";
  sort: string;
  descending: boolean;
  pageSize: number;
}>();
const query = defineModel<string>("query", { required: true });
const kind = defineModel<string>("kind", { required: true });
const state = defineModel<string>("state", { required: true });
const emit = defineEmits<{
  sort: [key: string];
  filter: [];
  page: [direction: 1 | -1];
  enter: [row: ExplorerRow];
  act: [action: string, row: ExplorerRow];
  batch: [action: "fetch" | "export", rows: ExplorerRow[]];
  retry: [];
}>();

const COLUMNS = [
  { key: "name", label: "名称", cls: "" },
  { key: "kind", label: "类型", cls: "" },
  { key: "size", label: "大小", cls: "num" },
  { key: "duration", label: "时长", cls: "num optional" },
  { key: "dimensions", label: "尺寸", cls: "num optional" },
  { key: "published", label: "发布时间", cls: "optional" },
  { key: "site", label: "站点", cls: "optional" },
  { key: "author", label: "作者", cls: "optional" },
];
const STATES = computed(() =>
  props.side === "downloads"
    ? [
        { value: "", label: "全部状态" },
        { value: "present", label: "完好" },
        { value: "changed", label: "被修改" },
        { value: "missing", label: "缺失" },
        { value: "untracked", label: "TRACE 没有记录" },
      ]
    : [
        { value: "", label: "全部状态" },
        { value: "complete", label: "已保存" },
        { value: "partial", label: "部分保存" },
        { value: "pending", label: "待保存" },
        { value: "deferred", label: "未下载（按订阅设置）" },
      ],
);
const checked = reactive(new Set<string>());
const confirming = ref<string | null>(null);
watch(
  () => props.list?.path,
  () => {
    checked.clear();
    confirming.value = null;
  },
);
const rows = computed(() => props.list?.rows ?? []);
const selectedRows = computed(() => rows.value.filter((r) => checked.has(r.key) && r.entry_id));
const first = computed(() => (props.list && props.list.total ? props.list.offset + 1 : 0));
const last = computed(() => (props.list ? props.list.offset + props.list.rows.length : 0));
const ariaSort = (key: string) =>
  props.sort === key ? (props.descending ? "descending" : "ascending") : "none";
const dimensions = (r: ExplorerRow) => (r.width && r.height ? `${r.width}×${r.height}` : "");
const fetchable = (r: ExplorerRow) =>
  Boolean(r.entry_id) && ["deferred", "partial", "pending"].includes(r.state) && r.node !== null;
/** Typing is searched when the input method has committed its text, not mid-composition. */
function onQuery(event: Event) {
  if ((event as InputEvent).isComposing) return;
  emit("filter");
}
function forget(row: ExplorerRow) {
  if (confirming.value !== row.key) {
    confirming.value = row.key;
    return;
  }
  confirming.value = null;
  emit("act", "forget", row);
}
</script>

<template>
  <section class="explorer-list" aria-label="文件列表">
    <div class="filters row-line">
      <label class="field grow">
        <span>名称包含</span>
        <input
          v-model="query"
          autocomplete="off"
          placeholder="输入文字筛选当前文件夹"
          aria-label="按名称筛选"
          @input="onQuery"
        />
      </label>
      <label class="field">
        <span>类型</span>
        <select v-model="kind" aria-label="按类型筛选" @change="emit('filter')">
          <option v-for="o in CONTENT_KIND_OPTIONS" :key="o.value" :value="o.value">
            {{ o.value ? o.label : "全部类型" }}
          </option>
        </select>
      </label>
      <label class="field">
        <span>状态</span>
        <select v-model="state" aria-label="按状态筛选" @change="emit('filter')">
          <option v-for="o in STATES" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
      </label>
    </div>

    <div v-if="selectedRows.length" class="row-line batch" role="group" aria-label="批量操作">
      <span>已选 {{ selectedRows.length }} 项</span>
      <button class="tool-button" @click="emit('batch', 'fetch', selectedRows)">补下载</button>
      <button class="tool-button" @click="emit('batch', 'export', selectedRows)">
        导出到默认下载文件夹
      </button>
    </div>

    <p v-if="list?.truncated" class="state-box" role="status">
      这个文件夹里的文件太多，只读取了前一部分；排序和翻页只覆盖已读取的部分。
    </p>
    <div v-if="error" class="state-box" role="alert">
      <span>无法读取列表：{{ error }}</span>
      <button class="tool-button" @click="emit('retry')">重试</button>
    </div>
    <p v-else-if="loading && !list" class="page-note" role="status">正在读取…</p>
    <div v-else-if="list && !rows.length" class="state-box">
      {{ query || kind || state ? "没有符合筛选条件的内容。" : "这里还没有内容。" }}
    </div>

    <div v-else-if="list" class="scroll-x">
      <table class="plain-table" :aria-busy="loading">
        <caption class="sr-only">
          {{
            list.path
          }}
        </caption>
        <thead>
          <tr>
            <th scope="col"><span class="sr-only">选择</span></th>
            <th
              v-for="c in COLUMNS"
              :key="c.key"
              scope="col"
              :class="c.cls"
              :aria-sort="ariaSort(c.key)"
            >
              <button class="sort-button" @click="emit('sort', c.key)">
                {{ c.label
                }}<span aria-hidden="true">{{
                  sort === c.key ? (descending ? " ▼" : " ▲") : ""
                }}</span>
              </button>
            </th>
            <th scope="col">状态</th>
            <th scope="col">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.key">
            <td>
              <input
                v-if="row.entry_id"
                type="checkbox"
                :checked="checked.has(row.key)"
                :aria-label="'选择 ' + displayText(row.name)"
                @change="
                  ($event.target as HTMLInputElement).checked
                    ? checked.add(row.key)
                    : checked.delete(row.key)
                "
              />
            </td>
            <td>
              <button
                v-if="row.node"
                class="name-button"
                :title="displayText(row.name)"
                @click="emit('enter', row)"
              >
                <bdi class="untrusted-text">{{ displayText(row.name) }}</bdi>
              </button>
              <bdi v-else class="untrusted-text" :title="displayText(row.name)">{{
                displayText(row.name)
              }}</bdi>
              <span v-if="hasHiddenCharacters(row.name)" class="badge warning">含隐藏字符</span>
              <div v-if="row.kinds.length > 1" class="page-note">
                {{ row.kinds.join(" + ") }}
              </div>
            </td>
            <td>{{ entryKindLabel(row.kind) }}</td>
            <td class="num">{{ row.size === null ? "" : formatBytes(row.size) }}</td>
            <td class="num optional">{{ formatDuration(row.duration_ms) }}</td>
            <td class="num optional">{{ dimensions(row) }}</td>
            <td class="optional">{{ row.published_at ? formatTime(row.published_at) : "" }}</td>
            <td class="optional">{{ row.site ? siteLabel(row.site) : "" }}</td>
            <td class="optional">
              <span class="untrusted-text">{{ displayText(row.author) }}</span>
            </td>
            <td>
              <span
                :class="[
                  'badge',
                  ['changed', 'missing', 'deferred'].includes(row.state) ? 'warning' : '',
                ]"
                >{{ copyStateLabel(row.state) }}</span
              >
            </td>
            <td class="actions">
              <button
                v-if="row.blob_id && ['image', 'video', 'audio'].includes(row.kind)"
                class="tool-button"
                :aria-label="'预览 ' + displayText(row.name)"
                @click="emit('act', 'preview', row)"
              >
                预览
              </button>
              <button
                v-if="row.can_open"
                class="tool-button"
                :aria-label="'打开 ' + displayText(row.name)"
                @click="emit('act', 'open', row)"
              >
                打开
              </button>
              <button
                v-if="row.can_reveal"
                class="tool-button"
                :aria-label="'在文件夹中显示 ' + displayText(row.name)"
                @click="emit('act', 'reveal', row)"
              >
                显示位置
              </button>
              <button
                v-if="row.path"
                class="tool-button"
                :aria-label="'复制路径 ' + displayText(row.name)"
                @click="emit('act', 'copy', row)"
              >
                复制路径
              </button>
              <button
                v-if="row.can_sync"
                class="tool-button"
                :aria-label="'同步到资源库 ' + displayText(row.name)"
                @click="emit('act', 'sync', row)"
              >
                同步到资源库
              </button>
              <button
                v-if="fetchable(row)"
                class="tool-button"
                :aria-label="'补下载 ' + displayText(row.name)"
                @click="emit('act', 'fetch', row)"
              >
                补下载
              </button>
              <button
                v-if="row.can_forget"
                class="tool-button"
                :aria-label="'忘记记录 ' + displayText(row.name)"
                @click="forget(row)"
              >
                {{ confirming === row.key ? "确认忘记（文件不动）" : "忘记记录" }}
              </button>
              <router-link
                v-if="row.entry_id && side === 'library'"
                class="tool-button link"
                :to="{ path: '/', query: { entry: row.entry_id } }"
                :aria-label="'在时间线中查看 ' + displayText(row.name)"
                >时间线</router-link
              >
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <footer v-if="list && list.total > 0" class="row-line pager">
      <span class="page-note" role="status"
        >第 {{ first }}–{{ last }} 条，共 {{ list.total }} 条</span
      >
      <button
        class="tool-button"
        :disabled="loading || list.offset === 0"
        @click="emit('page', -1)"
      >
        上一页
      </button>
      <button
        class="tool-button"
        :disabled="loading || list.offset + pageSize >= list.total"
        @click="emit('page', 1)"
      >
        下一页
      </button>
    </footer>
  </section>
</template>

<style scoped>
.explorer-list {
  display: grid;
  gap: 8px;
  min-width: 0;
}
.filters select {
  min-width: 120px;
}
.sort-button,
.name-button {
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  font-weight: 600;
  cursor: pointer;
  max-width: 100%;
  text-align: left;
}
.name-button {
  color: var(--trace-blue, #3979ff);
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.actions .tool-button {
  min-height: 26px;
  padding: 0 8px;
  font-size: 0.75rem;
}
.link {
  display: inline-flex;
  align-items: center;
  text-decoration: none;
  color: inherit;
}
.batch {
  padding: 6px 10px;
  border-radius: 6px;
  background: #f4f8ff;
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}
@media (max-width: 900px) {
  .optional {
    display: none;
  }
}
</style>
