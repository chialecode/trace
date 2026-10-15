<script setup lang="ts">
import { ref, watch } from "vue";
import { api, type DeletePreview } from "../lib/api";
const props = defineProps<{ entryId: string }>();
const emit = defineEmits<{ changed: [] }>();
const scope = ref<"cache" | "copies" | "archive">("cache"),
  preview = ref<DeletePreview | null>(null),
  busy = ref(false),
  error = ref(""),
  result = ref("");
let version = 0;
watch(
  () => [props.entryId, scope.value],
  () => {
    version++;
    preview.value = null;
    error.value = "";
    result.value = "";
    busy.value = false;
  },
);
async function inspect() {
  const current = ++version;
  busy.value = true;
  error.value = "";
  try {
    const value = await api.deletePreview(props.entryId, scope.value);
    if (current === version) preview.value = value;
  } catch {
    if (current === version) error.value = "无法计算影响范围，请重试。";
  } finally {
    if (current === version) busy.value = false;
  }
}
async function remove() {
  if (!preview.value) return;
  const current = ++version;
  const p = preview.value;
  busy.value = true;
  error.value = "";
  try {
    await api.archiveDelete(p.entry_id, p.scope, p.token);
    if (current !== version) return;
    result.value = "已记录删除代次，符合条件的副本将在等待期后回收。";
    preview.value = null;
    emit("changed");
  } catch {
    if (current === version) {
      error.value = "范围已变化或操作未完成，请重新查看影响。";
      preview.value = null;
    }
  } finally {
    if (current === version) busy.value = false;
  }
}
</script>
<template>
  <details class="removal">
    <summary>档案与副本管理</summary>
    <label
      >操作范围
      <select v-model="scope" :disabled="busy">
        <option value="cache">删除本条派生播放缓存</option>
        <option value="copies">删除本条本地副本</option>
        <option value="archive">将本条档案移到回收站</option>
      </select></label
    >
    <p>
      停订阅请到创作者页操作。删副本保留正文和来源；删档案会从浏览与后续采集中隐藏。共享引用、手动保留和外部原文件受到保护。
    </p>
    <button :disabled="busy" @click="inspect">查看影响范围</button>
    <div v-if="preview">
      <p>
        {{ preview.revisions }} 个正文版本，{{ preview.variants }} 个媒体变体；其中
        {{ preview.pinned_preserved }} 个手动保留。等待
        {{ Math.ceil(preview.wait_seconds / 3600) }}
        小时后按共享引用和使用状态回收，实际释放空间可能为零。
      </p>
      <button :disabled="busy" @click="remove">确认此范围</button>
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="result" role="status">{{ result }}</p>
  </details>
</template>
<style scoped>
.removal {
  padding: 12px;
  border-top: 1px solid #e6edf5;
  font-size: 0.8125rem;
}
.removal p {
  line-height: 1.6;
}
.removal button,
.removal select {
  padding: 6px;
  margin: 4px;
  border: 1px solid #cbd8e6;
  border-radius: 6px;
  background: white;
  color: #25435f;
}
</style>
