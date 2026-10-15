<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import {
  NButton,
  NCard,
  NDataTable,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { h } from "vue";
import { api, type PluginDto } from "../lib/api";

const message = useMessage();
const items = ref<PluginDto[]>([]);
const loading = ref(false);
const changing = ref<string | null>(null);
let timer: number | undefined;
function enabled(row: PluginDto) { return !['disabled', 'stopping'].includes(row.state); }

async function refresh() {
  loading.value = true;
  try {
    items.value = (await api.pluginList()).items;
  } catch (e) {
    message.error(String(e));
  } finally {
    loading.value = false;
  }
}

async function toggle(row: PluginDto) {
  if (changing.value || row.state === 'stopping') return;
  changing.value = row.plugin_id;
  try {
    await api.pluginSetState(row.plugin_id, !enabled(row));
    await refresh();
  } catch (e) {
    message.error(String(e));
  } finally {
    changing.value = null;
  }
}

const columns: DataTableColumns<PluginDto> = [
  { title: "模块", key: "plugin_id" },
  { title: "平台", key: "platform", width: 120 },
  {
    title: "状态",
    key: "state",
    width: 110,
    render: (r) =>
      h(
        NTag,
        { type: r.state === "enabled" ? "success" : r.state === "disabled" ? "default" : "warning", size: "small" },
        { default: () => changing.value === r.plugin_id ? (enabled(r) ? '正在停止调度' : '正在启用') : ({enabled:'已启用',ready:'已就绪',installed:'已安装并启用',disabled:'已停用',stopping:'安全收尾中'}[r.state] ?? r.state) },
      ),
  },
  { title: '执行与收尾', key: 'active_tasks', render: r => `${r.active_tasks ?? 0} 项任务` },
  {
    title: "能力",
    key: "capabilities",
    render: (r) => (r.capabilities ?? []).join("、") || "-",
  },
  {
    title: "",
    key: "actions",
    width: 120,
    render: (r) =>
      h(
        NButton,
        { size: "small", disabled: !!changing.value || r.state === 'stopping', onClick: () => toggle(r) },
        { default: () => (r.state === 'stopping' ? '正在收尾' : enabled(r) ? "停用" : "启用") },
      ),
  },
];

onMounted(() => {
  void refresh();
  timer = window.setInterval(() => { if (items.value.some(item => item.state === 'stopping') && !loading.value) void refresh(); }, 1000);
});
onBeforeUnmount(() => window.clearInterval(timer));
</script>

<template>
  <n-card title="模块">
    <template #header-extra>
      <n-button size="small" @click="refresh">刷新</n-button>
    </template>
    <n-data-table :columns="columns" :data="items" :loading="loading" size="small" />
    <p class="hint">
      停用先撤销调度，已有下载和录制安全收尾，剩余任务数归零后显示「已停用」。重新启用后恢复等待任务；已保存档案仍可读取。适配器进程由核心受限代理隔离，Cookie 不进入模块进程。
    </p>
  </n-card>
</template>

<style scoped>
.hint {
  margin-top: 12px;
  color: #888;
  font-size: 12px;
}
</style>
