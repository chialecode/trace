<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, type DeliveryView } from "../lib/api";
import { formatTime } from "../lib/format";
import { displayText } from "../lib/safe";
import { DELIVERY_ITEM_LABELS, DELIVERY_STATE_LABELS, siteLabel } from "../lib/sites";
import "../styles/pages.css";

const props = defineProps<{
  /** Changes whenever a new delivery was started, so the panel reads again at once. */
  refreshKey?: string | number;
  pollMs?: number;
}>();

const items = ref<DeliveryView[]>([]);
const error = ref("");
const loaded = ref(false);
const note = ref("");
let timer: ReturnType<typeof setTimeout> | undefined;
let alive = true;
let request = 0;

const busy = computed(() => items.value.some((d) => ["running", "finishing"].includes(d.state)));

async function load() {
  const current = ++request;
  clearTimeout(timer);
  try {
    const list = await api.deliveries(undefined, 10);
    if (!alive || current !== request) return;
    items.value = list.items;
    error.value = "";
  } catch (e) {
    if (alive && current === request) error.value = String(e);
  } finally {
    if (alive && current === request) {
      loaded.value = true;
      if (busy.value) timer = setTimeout(load, props.pollMs ?? 2000);
    }
  }
}
async function cancel(delivery: DeliveryView) {
  note.value = "";
  try {
    await api.deliveryCancel(delivery.delivery_id);
    note.value = "已请求取消：已经写入的内容会保留，未开始的不再下载。";
  } catch (e) {
    note.value = String(e);
  }
  await load();
}
const target = (d: DeliveryView) =>
  d.target.kind === "folder"
    ? `资源库 + 下载文件夹${d.target.dir ? "（" + displayText(d.target.dir) + "）" : ""}`
    : "仅资源库";
const summary = (d: DeliveryView) => {
  const count = (state: string) => d.items.filter((i) => i.state === state).length;
  return `共 ${d.items.length} 条：完成 ${count("ready") + count("exported")}，部分 ${count("partial")}，失败 ${count("failed")}，进行中 ${count("queued")}`;
};
watch(() => props.refreshKey, load);
onMounted(load);
onBeforeUnmount(() => {
  alive = false;
  ++request;
  clearTimeout(timer);
});
</script>

<template>
  <section class="deliveries" aria-label="下载进度">
    <header class="row-line">
      <h3>最近的下载</h3>
      <button class="tool-button" @click="load">刷新</button>
    </header>
    <p v-if="note" class="page-note" role="status">{{ note }}</p>
    <div v-if="error" class="state-box" role="alert">
      <span>无法读取下载进度：{{ error }}</span>
      <button class="tool-button" @click="load">重试</button>
    </div>
    <p v-else-if="loaded && !items.length" class="page-note">还没有批量下载。</p>
    <article v-for="d in items" :key="d.delivery_id" class="delivery">
      <header class="row-line">
        <strong>{{ siteLabel(d.platform) }}</strong>
        <span class="badge">{{ d.kind === "profile" ? "勾选下载" : "单链接" }}</span>
        <span
          :class="[
            'badge',
            d.state === 'done' ? 'success' : d.state === 'cancelled' ? 'warning' : '',
          ]"
          >{{ DELIVERY_STATE_LABELS[d.state] ?? d.state }}</span
        >
        <span class="page-note">{{ formatTime(d.created_at) }} · {{ target(d) }}</span>
        <button
          v-if="d.state === 'running'"
          class="tool-button"
          :aria-label="'取消下载 ' + displayText(d.source)"
          @click="cancel(d)"
        >
          取消
        </button>
      </header>
      <p class="page-note untrusted-text">{{ displayText(d.source) }}</p>
      <p class="page-note">{{ summary(d) }}</p>
      <details v-if="d.items.length">
        <summary>逐条结果</summary>
        <div class="scroll-x">
          <table class="plain-table">
            <tbody>
              <tr v-for="i in d.items" :key="i.native_key">
                <td>
                  <span class="untrusted-text">{{
                    displayText(i.title) || displayText(i.native_key)
                  }}</span>
                </td>
                <td>
                  <span
                    :class="[
                      'badge',
                      i.state === 'failed' ? 'error' : i.state === 'partial' ? 'warning' : '',
                    ]"
                    >{{ DELIVERY_ITEM_LABELS[i.state] ?? i.state }}</span
                  >
                </td>
                <td class="page-note">
                  {{ i.message ? displayText(i.message) : i.files ? `${i.files} 个文件` : "" }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </details>
    </article>
  </section>
</template>

<style scoped>
.deliveries {
  display: grid;
  gap: 8px;
}
.delivery {
  display: grid;
  gap: 4px;
  padding: 10px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #fff;
}
.delivery p {
  margin: 0;
}
</style>
