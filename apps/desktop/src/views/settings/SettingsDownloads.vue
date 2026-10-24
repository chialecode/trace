<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, type DownloadSettingsView } from "../../lib/api";
import { formatBytes, formatTime } from "../../lib/format";

const settings = ref<DownloadSettingsView | null>(null);
const folder = ref("");
const error = ref("");
const note = ref("");
const busy = ref(false);

async function load() {
  try {
    settings.value = await api.downloadSettings();
    folder.value = settings.value.default_dir;
    error.value = "";
  } catch (e) {
    error.value = `无法读取下载位置：${String(e)}`;
  }
}
async function save(change: { default_dir?: string; sidecar?: boolean }, done: string) {
  if (busy.value) return;
  busy.value = true;
  note.value = "";
  try {
    settings.value = await api.downloadSettingsSet(change);
    folder.value = settings.value.default_dir;
    note.value = done;
  } catch (e) {
    note.value = String(e);
    if (settings.value) folder.value = settings.value.default_dir;
  } finally {
    busy.value = false;
  }
}
function saveFolder() {
  const path = folder.value.trim();
  if (!path || path === settings.value?.default_dir) return;
  void save({ default_dir: path }, "默认下载文件夹已更新；之前的文件不会被移动。");
}
function toggleSidecar(event: Event) {
  const on = (event.target as HTMLInputElement).checked;
  void save(
    { sidecar: on },
    on ? "之后写入的文件旁会附同名 .json 说明。" : "之后不再写入同名 .json 说明。",
  );
}
async function check(locationId: string) {
  note.value = "";
  try {
    const report = await api.downloadCheck({ locationId });
    note.value =
      `检查了 ${report.checked} 个文件：完好 ${report.present}，被修改 ${report.changed}，` +
      `缺失 ${report.missing}。检查只核对大小与记录，不会改动任何文件。`;
  } catch (e) {
    note.value = String(e);
  }
}
onMounted(load);
</script>

<template>
  <section class="settings-section" aria-labelledby="set-downloads">
    <h3 id="set-downloads">下载位置与命名</h3>
    <p class="page-note">
      下载总是先写入资源库；选择了下载文件夹时，再额外写一份可直接使用的文件。默认命名为“站点/作者/发布日期
      标题
      [内容ID].扩展名”；一条内容有多个文件时建同名文件夹并按顺序编号。名字里的特殊字符会被替换、过长会截短，重名时加区分后缀，不会覆盖你已有的文件。
    </p>
    <p v-if="error" class="state-box" role="alert">
      {{ error }} <button class="tool-button" @click="load">重试</button>
    </p>
    <template v-if="settings">
      <div class="row-line">
        <label class="field grow">
          <span>默认下载文件夹（完整路径）</span>
          <input
            v-model="folder"
            autocomplete="off"
            aria-label="默认下载文件夹"
            @keydown.enter="(e) => !e.isComposing && saveFolder()"
          />
        </label>
        <button class="tool-button primary" :disabled="busy" @click="saveFolder">保存</button>
      </div>
      <label class="check-line">
        <input
          type="checkbox"
          :checked="settings.sidecar"
          :disabled="busy"
          @change="toggleSidecar"
        />
        在每个下载文件旁写一份同名 .json（清洗后的标题、作者、标签、数据）
      </label>
      <ul v-if="settings.recent.length" class="recent" aria-label="用过的下载文件夹">
        <li v-for="item in settings.recent" :key="item.location_id">
          <code class="untrusted-text">{{ item.path }}</code>
          <span v-if="item.is_default" class="badge success">默认</span>
          <span v-if="!item.exists" class="badge warning">文件夹不存在</span>
          <span class="page-note"
            >可用 {{ formatBytes(item.free_bytes) }} · 最近使用
            {{ formatTime(item.last_used_at) }}</span
          >
          <button class="tool-button" @click="check(item.location_id)">检查文件</button>
        </li>
      </ul>
      <p v-else class="page-note">还没有用过下载文件夹。</p>
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
.check-line {
  display: flex;
  gap: 8px;
  align-items: center;
  font-size: 0.8125rem;
}
.recent {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.recent li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
</style>
