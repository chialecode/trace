<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NSelect,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { h } from "vue";
import { api, type JobDto } from "../lib/api";
import { formatTime, jobStateLabel, jobStateTagType } from "../lib/format";

const message = useMessage();
const items = ref<JobDto[]>([]);
const loading = ref(false);
const stateFilter = ref<string | null>(null);
const typeFilter = ref<string | null>(null);
let timer: number | undefined;

const stateOptions = [
  { label: "全部状态", value: "" },
  { label: "排队中", value: "queued" },
  { label: "执行中", value: "running" },
  { label: "等待重试", value: "waiting_retry" },
  { label: "等待登录", value: "waiting_auth" },
  { label: "等待模块", value: "waiting_plugin" },
  { label: "已成功", value: "succeeded" },
  { label: "已失败", value: "failed" },
  { label: "已取消", value: "cancelled" },
];
const typeOptions = [
  { label: "全部类型", value: "" },
  { label: "扫描", value: "scan" },
  { label: "详情观察", value: "observe" },
  { label: "媒体保存", value: "media" },
];

async function refresh() {
  loading.value = true;
  try {
    items.value = (
      await api.jobList(
        stateFilter.value || undefined,
        typeFilter.value || undefined,
      )
    ).items;
  } catch (e) {
    message.error(String(e));
  } finally {
    loading.value = false;
  }
}

async function retry(jobId: string) {
  try {
    await api.jobRetry(jobId);
    message.success("已重新入队");
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

async function cancel(jobId: string) {
  try {
    await api.jobCancel(jobId);
    message.success("已请求取消");
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

const columns: DataTableColumns<JobDto> = [
  { title: "类型", key: "job_type", width: 90 },
  {
    title: "状态",
    key: "state",
    width: 120,
    render: (r) =>
      h(NTag, { type: jobStateTagType(r.state), size: "small" }, { default: () => jobStateLabel(r.state) }),
  },
  { title: "尝试", key: "attempts", width: 60 },
  { title: "对象", key: "subject_id", width: 220, ellipsis: { tooltip: true } },
  {
    title: "下次运行",
    key: "next_run_at",
    width: 170,
    render: (r) => formatTime(r.next_run_at),
  },
  {
    title: "最近错误",
    key: "last_error",
    minWidth: 200,
    ellipsis: { tooltip: true },
    render: (r) => r.last_error?.message ?? "-",
  },
  { title: "更新时间", key: "updated_at", width: 170, render: (r) => formatTime(r.updated_at) },
  {
    title: "",
    key: "actions",
    width: 140,
    render: (r) =>
      h("div", { class: "row-actions" }, [
        h(NButton, { size: "small", onClick: () => retry(r.job_id) }, { default: () => "重试" }),
        h(
          NButton,
          { size: "small", quaternary: true, onClick: () => cancel(r.job_id) },
          { default: () => "取消" },
        ),
      ]),
  },
];

onMounted(() => {
  refresh();
  timer = window.setInterval(refresh, 5000);
});
onBeforeUnmount(() => window.clearInterval(timer));
</script>

<template>
  <n-card title="任务">
    <div class="toolbar">
      <n-select v-model:value="stateFilter" :options="stateOptions" style="width: 150px" />
      <n-select v-model:value="typeFilter" :options="typeOptions" style="width: 150px" />
      <n-button type="primary" @click="refresh">刷新</n-button>
    </div>
    <n-alert v-if="items.some((j) => j.state === 'waiting_auth')" type="warning" title="存在等待登录的任务" style="margin-bottom: 12px">
      请到「账号与登录」完成凭证导入或重新验证；任务不会自动更换账号或绕过访问限制。
    </n-alert>
    <n-data-table :columns="columns" :data="items" :loading="loading" size="small" />
  </n-card>
</template>

<style scoped>
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}
.row-actions {
  display: flex;
  gap: 4px;
}
</style>
