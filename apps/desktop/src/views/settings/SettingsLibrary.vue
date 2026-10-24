<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { api, type LibraryStatus, type LocationView } from "../../lib/api";
import { formatBytes, formatTime } from "../../lib/format";

const status = ref<LibraryStatus | null>(null);
const error = ref("");
const note = ref("");
const busy = ref(false);
const target = ref("");
const confirmRelease = ref<string | null>(null);
let timer: ReturnType<typeof setTimeout> | undefined;
let alive = true;

const MIGRATION_STATES: Record<string, string> = {
  copying: "正在复制",
  verifying: "正在逐个校验",
  ready: "已校验，等待切换",
  switched: "已切换到新位置",
  failed: "迁移失败",
  cancelled: "已取消",
  released: "旧位置已释放",
};
const OPEN = new Set(["copying", "verifying", "ready"]);

function apply(next: LibraryStatus) {
  status.value = next;
  clearTimeout(timer);
  const state = next.migration?.state ?? "";
  if (alive && (state === "copying" || state === "verifying")) timer = setTimeout(load, 1500);
}
async function load() {
  try {
    apply(await api.libraryStatus());
    error.value = "";
  } catch (e) {
    if (alive) error.value = `无法读取资源库位置：${String(e)}`;
  }
}
async function run(action: () => Promise<LibraryStatus>, done: string) {
  if (busy.value) return;
  busy.value = true;
  note.value = "";
  try {
    apply(await action());
    note.value = done;
  } catch (e) {
    note.value = String(e);
  } finally {
    busy.value = false;
  }
}
const migrate = () => {
  const path = target.value.trim();
  if (!path) {
    note.value = "请先填写新位置的完整路径（一个空文件夹，或尚不存在的文件夹）。";
    return;
  }
  void run(() => api.libraryMigrate(path), "已开始迁移：旧位置在你确认前保持完整可用。");
};
const switchNow = () => run(() => api.librarySwitch(), "已切换：之后的新内容写入新位置。");
const cancel = () => run(() => api.libraryCancel(), "已取消迁移，当前位置不变。");
function release(location: LocationView) {
  if (confirmRelease.value !== location.location_id) {
    confirmRelease.value = location.location_id;
    return;
  }
  confirmRelease.value = null;
  void run(
    () => api.libraryReleaseOld(location.location_id),
    "已释放旧位置，其中的文件可由你自行删除。",
  );
}
onMounted(load);
onBeforeUnmount(() => {
  alive = false;
  clearTimeout(timer);
});
</script>

<template>
  <section class="settings-section" aria-labelledby="set-library">
    <h3 id="set-library">资源库位置</h3>
    <p class="page-note">
      资源库保存全部已归档的内容。换位置时，TRACE
      先把全部内容复制到新位置并逐个校验，你确认后才切换；旧位置会继续保留，直到你主动释放。
    </p>
    <p v-if="error" class="state-box" role="alert">
      {{ error }} <button class="tool-button" @click="load">重试</button>
    </p>
    <template v-if="status">
      <p>
        当前位置：<code class="untrusted-text path">{{ status.active.path }}</code>
        <span class="page-note">
          · 可用空间 {{ formatBytes(status.active.free_bytes) }}
          <template v-if="!status.active.exists"> · 文件夹当前不存在</template>
        </span>
      </p>
      <div v-if="status.migration" class="migration" role="status">
        <strong>{{ MIGRATION_STATES[status.migration.state] ?? status.migration.state }}</strong>
        <span class="page-note">
          {{ status.migration.done_items }} / {{ status.migration.total_items }} 项 ·
          {{ formatBytes(status.migration.done_bytes) }} /
          {{ formatBytes(status.migration.total_bytes) }}
        </span>
        <span class="page-note"
          >目标 <code class="untrusted-text path">{{ status.migration.to_path }}</code> ·
          {{ formatTime(status.migration.updated_at) }}</span
        >
        <p v-if="status.migration.message" class="page-note">{{ status.migration.message }}</p>
        <div v-if="OPEN.has(status.migration.state)" class="row-line">
          <button
            v-if="status.migration.state === 'ready'"
            class="tool-button primary"
            :disabled="busy"
            @click="switchNow"
          >
            切换到新位置
          </button>
          <button class="tool-button" :disabled="busy" @click="cancel">取消迁移</button>
        </div>
      </div>
      <div v-if="!status.migration || !OPEN.has(status.migration.state)" class="row-line">
        <label class="field grow">
          <span>新位置（完整路径）</span>
          <input
            v-model="target"
            autocomplete="off"
            placeholder="例如 E:\TRACE资源库"
            aria-label="新资源库位置"
            @keydown.enter="(e) => !e.isComposing && migrate()"
          />
        </label>
        <button class="tool-button primary" :disabled="busy" @click="migrate">开始迁移</button>
      </div>
      <ul v-if="status.old_kept.length" class="old-list" aria-label="保留的旧位置">
        <li v-for="old in status.old_kept" :key="old.location_id">
          <code class="untrusted-text path">{{ old.path }}</code>
          <span class="page-note">{{ old.exists ? "仍有完整副本" : "文件夹已不存在" }}</span>
          <button class="tool-button" :disabled="busy" @click="release(old)">
            {{ confirmRelease === old.location_id ? "确认释放（TRACE 不再引用）" : "释放旧位置" }}
          </button>
        </li>
      </ul>
    </template>
    <p v-if="note" class="page-note" role="status">{{ note }}</p>
  </section>
</template>

<style scoped>
.settings-section {
  display: grid;
  gap: 8px;
  padding: 14px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #fff;
}
.settings-section p {
  margin: 0;
}
.path {
  max-width: min(100%, 560px);
}
.migration {
  display: grid;
  gap: 4px;
  padding: 10px;
  border-radius: 6px;
  background: #f4f8ff;
}
.old-list {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.old-list li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
</style>
