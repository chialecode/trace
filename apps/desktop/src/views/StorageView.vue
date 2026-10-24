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
import { api, type DeletedEntry, type CacheStatus } from "../lib/api";
import { formatBytes } from "../lib/format";

const message = useMessage();
const usage = ref<Record<string, unknown> | null>(null);
const backups = ref<Record<string, unknown>[]>([]);
const loading = ref(false);
const cache = ref<CacheStatus>(),
  indexMiB = ref(4096),
  derivedMiB = ref(10240),
  cacheBusy = ref(false);
async function readCache() {
  cache.value = await api.cacheStatus();
  indexMiB.value = cache.value.index_budget_bytes / 1048576;
  derivedMiB.value = cache.value.derived_budget_bytes / 1048576;
}
async function cacheAction(scope: string, rebuild: boolean) {
  cacheBusy.value = true;
  try {
    cache.value = await api.cacheControl(scope, rebuild);
    message.success(
      rebuild ? "已安排按预算重建。" : "已清理缓存引用；媒体回收仍保护固定保留与活跃读取。",
    );
  } catch {
    message.error("缓存操作未完成，请重试。");
  } finally {
    cacheBusy.value = false;
  }
}
async function saveCache() {
  cacheBusy.value = true;
  try {
    cache.value = await api.cacheConfigure(
      Math.floor(indexMiB.value * 1048576),
      Math.floor(derivedMiB.value * 1048576),
    );
    message.success("预算已保存。");
  } catch {
    message.error("预算未保存，请检查数值。");
  } finally {
    cacheBusy.value = false;
  }
}
const deleted = ref<DeletedEntry[]>([]),
  deletedNext = ref<string | null>(null),
  showDeleted = ref(false);
async function loadDeleted(more = false) {
  try {
    const page = await api.deletedEntries(more ? (deletedNext.value ?? undefined) : undefined);
    deleted.value = more ? [...deleted.value, ...page.items] : page.items;
    deletedNext.value = page.next ?? null;
    showDeleted.value = true;
  } catch (e) {
    message.error(String(e));
  }
}
async function restoreEntry(entry: DeletedEntry) {
  try {
    await api.restoreDeleted(entry.entry_id, entry.generation);
    await api.qualityRecycle();
    await loadDeleted();
    message.success("已恢复档案显示。已回收的媒体需要在详情中重新补查。");
  } catch (e) {
    message.error(String(e));
  }
}

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
    await readCache();
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
async function recycle() {
  try {
    const result = await api.qualityRecycle();
    message.success(
      `已移入回收区 ${result.moved_to_trash} 个旧副本，清理 ${result.removed_from_trash} 个已复核副本。`,
    );
    await refresh();
  } catch {
    message.error("回收未完成，已保存文件的引用保护仍有效。");
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
    message.success(
      `恢复完成：${result.restored_objects} 个对象，${result.missing_objects} 个缺失；登录状态已隔离。`,
    );
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
        {
          size: "small",
          quaternary: true,
          onClick: () => (restoreBackup.value = String(r["backup_root"] ?? r["backup_id"] ?? "")),
        },
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
        {{ formatBytes(Number(usage["free_bytes"] ?? NaN)) }}（任务安全预留
        {{ formatBytes(Number(usage["reserved_bytes"] ?? NaN)) }}）
      </n-descriptions-item>
    </n-descriptions>
    <template #footer>
      <div class="actions">
        <n-button size="small" type="primary" @click="backupNow">立即备份</n-button>
        <n-button size="small" @click="recycle">回收到期旧副本</n-button>
        <n-button size="small" @click="showExport = true">导出</n-button>
        <n-button size="small" @click="showRestore = true">恢复</n-button>
      </div>
    </template>

    <n-alert v-if="restoredProfile" type="success" style="margin-top: 12px"
      >恢复 Profile：{{ restoredProfile }}。关闭窗口后用 TRACE_PROFILE
      指向此文件，再启动桌面程序浏览恢复档案。</n-alert
    >
    <section v-if="cache" aria-label="可再生成缓存">
      <h3>可再生成缓存</h3>
      <p>
        搜索索引估算 {{ formatBytes(cache.index_estimated_bytes) }}；兼容播放副本
        {{ formatBytes(cache.derived_bytes) }}。SQLite
        已分配页由数据库复用，清理索引不立即缩小数据库文件。当前未生成缩略图，缩略图缓存为
        0；来源封面和原图作为档案保留。
      </p>
      <p role="status">
        搜索：{{
          !cache.index_enabled
            ? "已清理并暂停"
            : cache.index_budget_blocked
              ? "预算不足，索引追赶已暂停"
              : cache.index_rebuilding
                ? "正在重建"
                : "已启用"
        }}；兼容播放缓存：{{
          cache.derived_enabled ? "已启用" : "已暂停"
        }}。清理搜索后正文仍可浏览，重新启用后按后台预算重建。
      </p>
      <form class="actions" @submit.prevent="saveCache">
        <label
          >索引预算 MiB<input
            v-model.number="indexMiB"
            type="number"
            min="1"
            max="1048576"
            required /></label
        ><label
          >兼容播放预算 MiB<input
            v-model.number="derivedMiB"
            type="number"
            min="0"
            max="1048576"
            required /></label
        ><n-button attr-type="submit" :disabled="cacheBusy">保存预算</n-button>
      </form>
      <div class="actions">
        <n-button :disabled="cacheBusy" @click="cacheAction('search', false)"
          >清理并暂停搜索索引</n-button
        ><n-button :disabled="cacheBusy" @click="cacheAction('search', true)">重建搜索索引</n-button
        ><n-button :disabled="cacheBusy" @click="cacheAction('playback', false)"
          >清理并暂停兼容播放缓存</n-button
        ><n-button :disabled="cacheBusy" @click="cacheAction('playback', true)"
          >启用兼容播放缓存</n-button
        ><n-button :disabled="cacheBusy" @click="cacheAction('thumbnails', false)"
          >清理缩略图缓存</n-button
        >
      </div>
    </section>
    <p><n-button size="small" @click="loadDeleted()">回收站与已停用副本</n-button></p>
    <section v-if="showDeleted">
      <p>
        恢复是明确重新收录操作，不自动恢复已取消的任务。副本若已回收，可在详情中补查。移到回收站保留删除记录和可恢复的正文元数据。
      </p>
      <article v-for="entry in deleted" :key="entry.entry_id">
        {{ entry.title ?? entry.entry_id }} · 代次 {{ entry.generation }}
        <n-button size="small" @click="restoreEntry(entry)">恢复显示与收录资格</n-button>
      </article>
      <n-button v-if="deletedNext" @click="loadDeleted(true)">更多</n-button>
    </section>
    <h4 class="sec">本地默认备份（最多显示 200 份，完成后刷新）</h4>
    <n-data-table :columns="backupColumns" :data="backups" :loading="loading" size="small" />

    <n-modal
      v-model:show="showExport"
      preset="card"
      title="导出档案"
      style="width: min(520px, 94vw)"
    >
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

    <n-modal
      v-model:show="showRestore"
      preset="card"
      title="从备份恢复"
      style="width: min(520px, 94vw)"
    >
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
  flex-wrap: wrap;
}
.sec {
  margin: 16px 0 8px;
}
</style>
