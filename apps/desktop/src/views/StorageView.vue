<script setup lang="ts">
import { onMounted, ref } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSwitch,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { h } from "vue";
import { api } from "../lib/api";
import { formatBytes } from "../lib/format";

const message = useMessage();
const usage = ref<Record<string, unknown> | null>(null);
const backups = ref<Record<string, unknown>[]>([]);
const loading = ref(false);

const showExport = ref(false);
const exportDir = ref("");
const exportMedia = ref(true);
const showRestore = ref(false);
const restoredProfile = ref("");
const restoreBackup = ref("");
const restoreTarget = ref("");

async function refresh() {
  loading.value = true;
  try {
    usage.value = await api.storageUsage();
    backups.value = (await api.backupList()).items;
  } catch (e) {
    message.error(String(e));
  } finally {
    loading.value = false;
  }
}

async function backupNow() {
  try {
    const result = await api.backupStart();
    message.success(`备份已排队，可在任务页查看结果：${result.job_id}`);
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

async function doExport() {
  if (!exportDir.value.trim()) {
    message.warning("请填写导出目标目录");
    return;
  }
  try {
    const r = await api.exportStart(exportDir.value.trim(), exportMedia.value);
    message.success(`导出已受理：${r.target}`);
    showExport.value = false;
  } catch (e) {
    message.error(String(e));
  }
}

async function doRestore() {
  if (!restoreBackup.value.trim() || !restoreTarget.value.trim()) {
    message.warning("请填写备份目录与目标目录（目标必须是空目录）");
    return;
  }
  try {
    const result = await api.restoreStart(restoreBackup.value.trim(), restoreTarget.value.trim());
    restoredProfile.value = result.profile_path;
    message.success(`恢复完成：${result.restored_objects} 个对象，${result.missing_objects} 个缺失；登录状态已隔离。`);
    showRestore.value = false;
  } catch (e) {
    message.error(String(e));
  }
}

const backupColumns: DataTableColumns<Record<string, unknown>> = [
  { title: "备份", key: "backup_id" },
  { title: "时间", key: "created_at", width: 180 },
  {
    title: "",
    key: "actions",
    width: 120,
    render: (r) =>
      h(
        NButton,
        { size: "small", quaternary: true, onClick: () => (restoreBackup.value = String(r["backup_root"] ?? r["backup_id"] ?? "")) },
        { default: () => "选择恢复源" },
      ),
  },
];

onMounted(refresh);
</script>

<template>
  <n-card title="存储与备份">
    <template #header-extra>
      <n-button size="small" @click="refresh">刷新</n-button>
    </template>
    <n-alert type="info" :bordered="false" style="margin-bottom: 12px">
      备份与导出不包含凭证；恢复后需要重新登录。恢复目标是独立目录，不覆盖现有档案。
    </n-alert>
    <n-descriptions v-if="usage" bordered :column="2" size="small">
      <n-descriptions-item label="对象存储">
        {{ formatBytes(Number(usage["objects_bytes"] ?? NaN)) }}
      </n-descriptions-item>
      <n-descriptions-item label="数据库">
        {{ formatBytes(Number(usage["db_bytes"] ?? NaN)) }}
      </n-descriptions-item>
      <n-descriptions-item label="暂存区">
        {{ formatBytes(Number(usage["staging_bytes"] ?? NaN)) }}
      </n-descriptions-item>
      <n-descriptions-item label="剩余磁盘">
        {{ formatBytes(Number(usage["free_bytes"] ?? NaN)) }}
      </n-descriptions-item>
    </n-descriptions>
    <template #footer>
      <div class="actions">
        <n-button size="small" type="primary" @click="backupNow">立即备份</n-button>
        <n-button size="small" @click="showExport = true">导出</n-button>
        <n-button size="small" @click="showRestore = true">恢复</n-button>
      </div>
    </template>

    <n-alert v-if="restoredProfile" type="success" style="margin-top:12px">恢复 Profile：{{ restoredProfile }}。关闭窗口后用 TRACE_PROFILE 指向此文件，再启动桌面程序浏览恢复档案。</n-alert>
    <h4 class="sec">本地默认备份（最多显示 200 份，完成后刷新）</h4>
    <n-data-table :columns="backupColumns" :data="backups" :loading="loading" size="small" />

    <n-modal v-model:show="showExport" preset="card" title="导出档案" style="width: 520px">
      <n-form>
        <n-form-item label="目标目录">
          <n-input v-model:value="exportDir" placeholder="如 D:\\trace-export" />
        </n-form-item>
        <n-form-item label="包含媒体文件">
          <n-switch v-model:value="exportMedia" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-button type="primary" @click="doExport">开始导出</n-button>
      </template>
    </n-modal>

    <n-modal v-model:show="showRestore" preset="card" title="从备份恢复" style="width: 520px">
      <n-form>
        <n-form-item label="备份目录（含清单）">
          <n-input v-model:value="restoreBackup" />
        </n-form-item>
        <n-form-item label="恢复目标（必须为空目录）">
          <n-input v-model:value="restoreTarget" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-button type="primary" @click="doRestore">开始恢复</n-button>
      </template>
    </n-modal>
  </n-card>
</template>

<style scoped>
.actions {
  display: flex;
  gap: 8px;
}
.sec {
  margin: 16px 0 8px;
}
</style>
