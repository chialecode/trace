<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { api, mediaUrl, type ExplorerNode, type ExplorerRow } from "../lib/api";
import { displayText } from "../lib/safe";
import { SYNC_STATE_LABELS } from "../lib/sites";
import ExplorerList from "./explorer/ExplorerList.vue";
import ExplorerTree from "./explorer/ExplorerTree.vue";
import { isDownloadSide, PAGE_SIZE, useExplorer } from "./explorer/useExplorer";
import "../styles/pages.css";

const ex = useExplorer();
const note = ref("");
const preview = ref<ExplorerRow | null>(null);

const side = computed(() =>
  ex.selected.value && isDownloadSide(ex.selected.value.node) ? "downloads" : "library",
);
const truncated = computed(() =>
  Object.fromEntries(Object.entries(ex.branches).map(([node, b]) => [node, b.truncated])),
);
const previewSrc = computed(() => (preview.value ? mediaUrl(preview.value.blob_id) : ""));

function enter(row: ExplorerRow) {
  if (!row.node) return;
  void ex.select({
    node: row.node,
    name: row.name,
    kind: row.kind === "folder" ? "folder" : "entry",
    count: null,
    expandable: false,
  } satisfies ExplorerNode);
}
async function report<T>(work: () => Promise<T>, say: (value: T) => string) {
  note.value = "";
  try {
    note.value = say(await work());
  } catch (e) {
    note.value = String(e);
  }
}
async function act(action: string, row: ExplorerRow) {
  const name = displayText(row.name);
  if (action === "preview") {
    preview.value = row;
  } else if (action === "open") {
    await report(
      () =>
        row.location_id && row.rel_path
          ? api.explorerOpen(row.location_id, row.rel_path)
          : api.openExternal(row.blob_id ?? ""),
      () => `已用系统默认程序打开 ${name}`,
    );
  } else if (action === "reveal" && row.location_id && row.rel_path) {
    await report(
      () => api.explorerReveal(row.location_id!, row.rel_path!),
      () => `已在文件夹中显示 ${name}`,
    );
  } else if (action === "copy") {
    await report(
      async () => {
        if (!navigator.clipboard) throw new Error("当前环境不能写入剪贴板，请手动选取路径。");
        await navigator.clipboard.writeText(row.path);
      },
      () => "路径已复制。",
    );
  } else if (action === "sync" && row.entry_id) {
    await report(
      () => api.downloadSync([row.entry_id!]),
      (r) => {
        const item = r.items[0];
        const text: Record<string, string> = {
          already_in_library: "资源库里已有相同的内容，没有重复复制。",
          restored: "已把下载文件夹里的文件补回资源库。",
          skipped: "没有同步",
        };
        return `${name}：${text[item?.state ?? "skipped"] ?? item?.state}${item?.message ? `（${item.message}）` : ""}`;
      },
    );
    await ex.loadList();
  } else if (action === "forget" && row.entry_id) {
    await report(
      () => api.downloadForget(row.entry_id!, row.location_id ?? undefined),
      (r) => `已忘记 ${r.removed} 条记录，文件本身没有被改动。`,
    );
    await ex.loadList();
  } else if (action === "fetch" && row.entry_id) {
    await report(
      () => api.archiveEntrySync([row.entry_id!]),
      (r) => `${name}：${SYNC_STATE_LABELS[r.items[0]?.state ?? "failed"] ?? r.items[0]?.state}`,
    );
  }
}
async function batch(action: "fetch" | "export", rows: ExplorerRow[]) {
  const ids = [...new Set(rows.map((r) => r.entry_id).filter((id): id is string => Boolean(id)))];
  if (!ids.length) return;
  if (action === "fetch")
    await report(
      () => api.archiveEntrySync(ids),
      (r) => {
        const counts = new Map<string, number>();
        for (const i of r.items) counts.set(i.state, (counts.get(i.state) ?? 0) + 1);
        return [...counts].map(([s, n]) => `${SYNC_STATE_LABELS[s] ?? s} ${n} 条`).join("；");
      },
    );
  else
    await report(
      () => api.downloadExport(ids),
      (r) => {
        const counts = new Map<string, number>();
        for (const i of r.items) counts.set(i.state, (counts.get(i.state) ?? 0) + 1);
        const text: Record<string, string> = {
          exported: "已写入",
          unchanged: "早已在文件夹中",
          nothing: "还没有可导出的文件",
          failed: "失败",
        };
        return [...counts].map(([s, n]) => `${text[s] ?? s} ${n} 条`).join("；");
      },
    );
}
onMounted(() => void ex.loadRoots());
</script>

<template>
  <div class="page-stack" data-page="explorer">
    <header class="row-line">
      <h2>资源管理器</h2>
      <p class="page-note grow">
        左边是“资源库”（按网站、作者、年月整理）和“下载文件夹”（你电脑上的真实文件）。名称来自网站或文件系统，一律按纯文字显示。
      </p>
      <button class="tool-button" @click="ex.loadList()">刷新</button>
    </header>
    <p v-if="note" class="page-note" role="status">{{ note }}</p>

    <div class="explorer-layout">
      <details class="tree-pane" open>
        <summary>文件夹</summary>
        <ExplorerTree
          :items="ex.visible.value"
          :selected="ex.selected.value?.node ?? null"
          :loading="ex.rootState.state === 'loading'"
          :error="ex.rootState.state === 'error' ? ex.rootState.error : ''"
          :truncated="truncated"
          @select="ex.select"
          @toggle="ex.toggle"
          @retry="ex.loadRoots"
        />
      </details>
      <div class="list-pane">
        <p v-if="ex.list.value" class="address" aria-label="当前位置">
          <bdi class="untrusted-text">{{ displayText(ex.list.value.path) }}</bdi>
        </p>
        <ExplorerList
          v-model:query="ex.query.value"
          v-model:kind="ex.kind.value"
          v-model:state="ex.state.value"
          :list="ex.list.value"
          :loading="ex.listLoading.value"
          :error="ex.listError.value"
          :side="side"
          :sort="ex.sort.value"
          :descending="ex.descending.value"
          :page-size="PAGE_SIZE"
          @sort="ex.sortBy"
          @filter="ex.refilter(250)"
          @page="ex.page"
          @enter="enter"
          @act="act"
          @batch="batch"
          @retry="ex.loadList"
        />
        <section v-if="preview" class="preview" aria-label="预览">
          <header class="row-line">
            <strong class="untrusted-text">{{ displayText(preview.name) }}</strong>
            <button class="tool-button" @click="preview = null">关闭预览</button>
          </header>
          <p v-if="!previewSrc" class="state-box">这个文件暂时不能预览。</p>
          <img
            v-else-if="preview.kind === 'image'"
            :src="previewSrc"
            :alt="displayText(preview.name)"
          />
          <audio v-else-if="preview.kind === 'audio'" :src="previewSrc" controls preload="none" />
          <video v-else :src="previewSrc" controls preload="metadata" />
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.explorer-layout {
  display: grid;
  grid-template-columns: minmax(200px, 280px) minmax(0, 1fr);
  gap: 14px;
  align-items: start;
}
.tree-pane {
  padding: 8px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #fff;
  max-height: calc(100vh - 220px);
  overflow: auto;
}
.tree-pane summary {
  margin-bottom: 6px;
  font-weight: 600;
  cursor: pointer;
}
.list-pane {
  display: grid;
  gap: 8px;
  min-width: 0;
  padding: 10px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #fff;
}
.address {
  margin: 0;
  color: #51627b;
  font-size: 0.8125rem;
}
.address bdi {
  max-width: 100%;
}
.preview {
  display: grid;
  gap: 8px;
  padding: 10px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #f8fbff;
}
.preview img,
.preview video {
  max-width: 100%;
  max-height: 60vh;
  object-fit: contain;
}
@media (max-width: 900px) {
  .explorer-layout {
    grid-template-columns: minmax(0, 1fr);
  }
  .tree-pane {
    max-height: 240px;
  }
}
</style>
