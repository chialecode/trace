<script setup lang="ts">
import { onMounted, ref } from "vue";
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
  try {
    await api.pluginSetState(row.plugin_id, row.state !== "enabled");
    refresh();
  } catch (e) {
    message.error(String(e));
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
        { default: () => (r.state === "enabled" ? "已启用" : r.state === "disabled" ? "已停用" : r.state) },
      ),
  },
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
        { size: "small", onClick: () => toggle(r) },
        { default: () => (r.state === "enabled" ? "停用" : "启用") },
      ),
  },
];

onMounted(refresh);
</script>

<template>
  <n-card title="模块">
    <template #header-extra>
      <n-button size="small" @click="refresh">刷新</n-button>
    </template>
    <n-data-table :columns="columns" :data="items" :loading="loading" size="small" />
    <p class="hint">
      停用的模块对应任务进入「等待模块」；重新启用后自动恢复执行。适配器进程由核心受限代理隔离，Cookie 不进入模块进程。
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
