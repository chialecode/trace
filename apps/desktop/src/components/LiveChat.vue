<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from "vue";
import { api } from "../lib/api";
const props = defineProps<{ sessionId: string }>();
type Page = Awaited<ReturnType<typeof api.liveAuxiliary>>;
const items = ref<Page["items"]>([]),
  latest = ref<Page["latest"]>(),
  enabled = ref(false),
  next = ref<number>(),
  total = ref(0),
  busy = ref(false),
  error = ref("");
let generation = 0;
async function load(older = false) {
  if (busy.value) return;
  const request = ++generation,
    sid = props.sessionId;
  busy.value = true;
  error.value = "";
  try {
    let cursor = older ? next.value : undefined;
    const oldest = items.value.at(-1)?.sequence;
    const merged = new Map(items.value.map((item) => [item.sequence, item]));
    do {
      const page = await api.liveAuxiliary(sid, cursor);
      if (request !== generation) return;
      for (const item of page.items) merged.set(item.sequence, item);
      if (cursor !== undefined && page.next_before != null && page.next_before >= cursor)
        throw new Error("cursor did not advance");
      total.value = page.total;
      latest.value = page.latest;
      enabled.value = page.enabled;
      cursor = page.next_before ?? undefined;
      if (older || oldest === undefined || cursor === undefined || cursor <= oldest) break;
    } while (true);
    items.value = [...merged.values()].sort((a, b) => b.sequence - a.sequence);
    next.value = cursor;
  } catch {
    if (request === generation) error.value = "弹幕采样未能读取，请重试。";
  } finally {
    if (request === generation) busy.value = false;
  }
}
watch(
  () => props.sessionId,
  () => {
    generation++;
    items.value = [];
    latest.value = undefined;
    next.value = undefined;
    total.value = 0;
    busy.value = false;
    error.value = "";
  },
);
onBeforeUnmount(() => generation++);
</script>
<template>
  <details @toggle="($event.currentTarget as HTMLDetailsElement).open && !items.length && load()">
    <summary>已保存的直播弹幕采样</summary>
    <p>历史窗口采样，可能缺少两次采样之间的消息。字幕若已嵌入画面，会随原始录制保留。</p>
    <p v-if="!enabled">本订阅未启用弹幕采样；已保存消息仍可离线阅读。</p>
    <p v-if="latest">
      最近采样：{{ latest.observed_at }} ·
      {{ latest.coverage === "failed" ? "获取失败，录制不受影响" : "有限历史窗口"
      }}<span v-if="latest.omitted"> · {{ latest.omitted }} 条其他类型消息未归档</span>
    </p>
    <p v-if="error" role="alert">{{ error }}</p>
    <button :disabled="busy" @click="load()">刷新弹幕</button>
    <p>已载入 {{ items.length }} / {{ total }} 条</p>
    <ul>
      <li v-for="item in items" :key="item.sequence">
        <time>{{ item.source_time || item.observed_at }}</time> ·
        {{ item.author || "未提供署名" }}：{{ item.text }}
      </li>
    </ul>
    <button v-if="next !== undefined" :disabled="busy" @click="load(true)">加载更早弹幕</button>
  </details>
</template>
<style scoped>
p,
li {
  font-size: 0.75rem;
}
ul {
  max-height: 280px;
  overflow: auto;
  padding-left: 18px;
}
li {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  margin: 8px 0;
}
button {
  padding: 6px 10px;
  color: #245fc8;
  background: #e9f1ff;
  border: 1px solid #bdcfea;
  border-radius: 6px;
}
</style>
