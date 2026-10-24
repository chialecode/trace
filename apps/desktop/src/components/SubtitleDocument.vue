<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from "vue";
import { mediaUrl, type MediaDto } from "../lib/api";
const props = defineProps<{ media: MediaDto }>();
const raw = ref("");
const lines = ref<{ from: number; to: number; content: string }[]>([]),
  shown = ref(100),
  error = ref(""),
  busy = ref(false);
let controller: AbortController | undefined;
let generation = 0;
watch(
  () => props.media.blob_id,
  () => {
    controller?.abort();
    generation++;
    raw.value = "";
    lines.value = [];
    shown.value = 100;
    error.value = "";
    busy.value = false;
  },
);
async function read() {
  const url = mediaUrl(props.media.blob_id);
  if (!url) return;
  if (!props.media.size || props.media.size > 8 * 1024 * 1024) {
    error.value = "字幕大小超出本地阅读范围。";
    return;
  }
  const current = ++generation;
  controller?.abort();
  controller = new AbortController();
  busy.value = true;
  error.value = "";
  try {
    const response = await fetch(url, { signal: controller.signal });
    if (!response.ok) throw new Error("missing");
    if (props.media.mime === "text/vtt") {
      const text = await response.text();
      if (current === generation) raw.value = text;
      return;
    }
    const data = await response.json();
    if (current !== generation) return;
    if (!Array.isArray(data.body) || data.body.length > 50000) throw new Error("invalid");
    lines.value = data.body.filter(
      (l: Record<string, unknown>) =>
        typeof l.content === "string" && typeof l.from === "number" && typeof l.to === "number",
    );
  } catch {
    if (current === generation) error.value = "本地字幕读取失败，请刷新资源后重试。";
  } finally {
    if (current === generation) busy.value = false;
  }
}
onBeforeUnmount(() => {
  generation++;
  controller?.abort();
});
</script>
<template>
  <section aria-label="离线字幕">
    <button :disabled="busy" @click="read">{{ busy ? "正在读取…" : "读取已保存字幕" }}</button>
    <p v-if="error" role="alert">{{ error }}</p>
    <pre v-if="raw">{{ raw }}</pre>
    <ol>
      <li v-for="(line, index) in lines.slice(0, shown)" :key="index">
        <time>{{ line.from.toFixed(2) }}–{{ line.to.toFixed(2) }} 秒</time>
        <p>{{ line.content }}</p>
      </li>
    </ol>
    <button v-if="shown < lines.length" @click="shown += 100">
      更多字幕（{{ shown }} / {{ lines.length }}）
    </button>
  </section>
</template>
<style scoped>
ol {
  max-height: 360px;
  overflow: auto;
  padding-left: 24px;
}
li {
  margin-bottom: 12px;
}
time {
  font-size: 0.6875rem;
  color: #60708a;
}
pre,
p {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
