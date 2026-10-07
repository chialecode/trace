<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { NAlert, NButton, NCard, NDescriptions, NDescriptionsItem, NTag, useMessage } from "naive-ui";
import { api, type DaemonStatus } from "../lib/api";
import { formatBytes, formatTime, jobStateLabel } from "../lib/format";

const message = useMessage();
const status = ref<DaemonStatus | null>(null);
const error = ref<string | null>(null);
const busy = ref(false);
let timer: number | undefined;

async function refresh() {
  try {
    status.value = await api.daemonStatus();
    error.value = null;
  } catch (e) {
    error.value = String(e);
  }
}

async function togglePause() {
  if (!status.value) return;
  busy.value = true;
  try {
    status.value = await api.daemonPause(!status.value.paused);
  } catch (e) {
    message.error(String(e));
  } finally {
    busy.value = false;
  }
}

onMounted(() => {
  refresh();
  timer = window.setInterval(refresh, 5000);
});
onBeforeUnmount(() => window.clearInterval(timer));
</script>

<template>
  <n-card title="后台状态">
    <template #header-extra>
      <n-button size="small" @click="refresh">刷新</n-button>
    </template>
    <n-alert v-if="error" type="error" title="无法连接独立后台">
      {{ error }}
      <p>请确认 trace-daemon 正在运行；桌面外壳会自动尝试拉起。</p>
    </n-alert>
    <n-descriptions v-else-if="status" bordered :column="2" size="small">
      <n-descriptions-item label="档案">{{ status.archive_id }}</n-descriptions-item>
      <n-descriptions-item label="实例">{{ status.instance_id }}</n-descriptions-item>
      <n-descriptions-item label="运行状态">
        <n-tag :type="status.paused ? 'warning' : 'success'">
          {{ status.paused ? "已暂停调度" : "调度中" }}
        </n-tag>
      </n-descriptions-item>
      <n-descriptions-item label="启动时间">{{ formatTime(status.started_at) }}</n-descriptions-item>
      <n-descriptions-item label="任务统计">
        <span v-for="(v, k) in status.job_counts" :key="k" class="count-item">
          {{ jobStateLabel(String(k)) }} {{ v }}
        </span>
        <span v-if="!Object.keys(status.job_counts).length">无任务</span>
      </n-descriptions-item>
      <n-descriptions-item label="媒体后端">
        {{ status.media_backend?.version ?? "未检测到 ffmpeg/ffprobe（外部打开仍可用）" }}
      </n-descriptions-item>
    </n-descriptions>
    <template #footer>
      <n-button size="small" :disabled="!status" :loading="busy" @click="togglePause">
        {{ status?.paused ? "恢复调度" : "暂停调度" }}
      </n-button>
      <span class="hint">
        对象存储：{{ formatBytes(Number(status?.storage?.objects_bytes ?? NaN)) }}
      </span>
    </template>
  </n-card>
</template>

<style scoped>
.count-item {
  margin-right: 12px;
}
.hint {
  margin-left: 12px;
  color: var(--n-text-color-3, #888);
  font-size: 12px;
}
</style>
