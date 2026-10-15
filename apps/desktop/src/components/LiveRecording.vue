<script setup lang="ts">
import { authLabel, actualSpec } from "../lib/acquisition";
import { ref, watch, onBeforeUnmount } from "vue";
import { api, type LiveDto, type FragmentDto } from "../lib/api";
import { formatTime } from "../lib/format";
import LiveChat from "./LiveChat.vue";
const props = defineProps<{ live: LiveDto; fragments: FragmentDto[] }>();
const emit = defineEmits<{ refresh: [] }>();
const busy = ref(false);
const error = ref("");
const items = ref<FragmentDto[]>([]);
const next = ref<number>();
const total = ref(0);
let request = 0;
const labels: Record<string, string> = {
  waiting: "等待录制",
  recording: "正在录制",
  reconnecting: "等待重连",
  stopping: "正在封片",
  sealed: "已封口",
  interrupted: "录制中断",
  failed: "录制失败",
  gap: "缺口",
  receiving: "接收中",
  source: "已保存原始数据",
  superseded: "已重新取得",
};
const reasons: Record<string, string> = {
  strict_quality_unrecorded: "实际规格不达标，严格策略未收录此区间",
  user_deleted: "用户移除",
  segments_slid_out_of_window: "来源窗口已移过未保存区间",
  source_declared_gap: "来源声明缺片",
  source_clock_reset: "来源时钟重置",
  codec_configuration_changed: "媒体编码参数变化",
  requested_stop: "手动停止",
  lifecycle_interrupted: "任务暂停或后台退出",
  idle_or_sleep_gap: "无数据或系统休眠",
  stream_disconnected: "连接中断",
  recovered_commit_window: "启动时完成封片恢复",
  recovered_by_retry: "重新取得同一来源分片",
  disk_budget_exhausted: "磁盘余量不足",
  rotation: "按分片预算轮转",
  invalid_flv_frame: "来源媒体结构异常",
};
async function load(older = false) {
  if (busy.value) return;
  const id = ++request,
    sid = props.live.session_id;
  busy.value = true;
  error.value = "";
  const oldest = items.value.at(-1)?.sequence;
  try {
    let cursor = older ? next.value : undefined;
    const merged = new Map(items.value.map((f) => [f.sequence, f]));
    do {
      const page = await api.liveFragments(sid, cursor);
      if (id !== request) return;
      for (const f of page.items) merged.set(f.sequence, f);
      total.value = page.total;
      cursor = page.next_before ?? undefined;
      if (older || oldest === undefined || cursor === undefined || cursor <= oldest) break;
    } while (true);
    items.value = [...merged.values()].sort((a, b) => b.sequence - a.sequence);
    next.value = cursor;
  } catch {
    if (id === request) error.value = "分片列表未能读取，请重试。";
  } finally {
    if (id === request) busy.value = false;
  }
}
async function toggle() {
  const sid = props.live.session_id;
  busy.value = true;
  error.value = "";
  try {
    await api.liveSetRecording(sid, props.live.manual_stopped, props.live.generation);
    if (props.live.session_id === sid) emit("refresh");
  } catch {
    if (props.live.session_id === sid) error.value = "录制状态未能更新，请刷新后重试。";
  } finally {
    if (props.live.session_id === sid) busy.value = false;
  }
}
async function open(f: FragmentDto) {
  if (f.blob_id)
    try {
      await api.openExternal(f.blob_id);
    } catch {
      error.value = "分片无法打开，文件可能缺失或系统播放器不支持。";
    }
}
watch(
  () => props.live.session_id,
  () => {
    request++;
    items.value = [];
    next.value = undefined;
    total.value = 0;
    busy.value = false;
    error.value = "";
  },
);
onBeforeUnmount(() => request++);
</script>
<template>
  <section class="live-recording" aria-label="录制场次">
    <h3>录制场次</h3>
    <p>
      {{ labels[live.state] ?? "状态未知" }} · 检测到本场 {{ formatTime(live.started_at)
      }}<span v-if="live.ended_at"> · 结束 {{ formatTime(live.ended_at) }}</span>
    </p>
    <p>每片显示获取当时的身份和实际规格；同场可能混合，未知字段保持未知。登录后不补录过去。</p>
    <p v-if="live.manual_stopped">已手动停止本场，自动检查不会重新开始录制。</p>
    <button :disabled="busy" @click="toggle">
      {{ busy ? "更新中…" : live.manual_stopped ? "手动继续本场" : "停止本场录制" }}
    </button>
    <p v-if="error" role="alert">{{ error }}</p>
    <details @toggle="($event.currentTarget as HTMLDetailsElement).open && !items.length && load()">
      <summary>分片与缺口</summary>
      <p v-if="total">已载入 {{ items.length }} / {{ total }} 项</p>
      <button :disabled="busy" @click="load()">刷新分片</button>
      <ul>
        <li v-for="f in items" :key="f.sequence">
          第 {{ f.sequence + 1 }} 项 · 来源时钟区间
          {{
            f.start_ms === undefined || f.start_ms === null
              ? "未知"
              : `${(f.start_ms / 1000).toFixed(2)}秒`
          }}
          ～
          {{
            f.end_ms === undefined || f.end_ms === null
              ? "未知"
              : `${(f.end_ms / 1000).toFixed(2)}秒`
          }}
          · {{ labels[f.state] ?? "状态未知" }} · 本机捕获 {{ formatTime(f.captured_at) }} · 保存
          {{ formatTime(f.saved_at)
          }}<span> · {{ authLabel(f.authentication) }} · {{ actualSpec(f.actual_spec) }}</span
          ><span v-if="f.reason"> · {{ reasons[f.reason] ?? "记录了异常或恢复说明" }}</span
          ><span v-if="f.quality?.requested_quality">
            · 期望 {{ f.quality.requested_quality.min_height ?? "未指定" }}p /
            {{ f.quality.requested_quality.min_fps ?? "未指定" }} fps；实际
            {{ f.quality.actual_quality?.height ?? "未知" }}p /
            {{ f.quality.actual_quality?.fps ?? "未知" }} fps
            {{ f.quality.below_target ? "（未达标）" : "" }}</span
          ><button v-if="f.available && f.blob_id" @click="open(f)">打开已保存分片</button>
        </li>
      </ul>
      <button v-if="next !== undefined" :disabled="busy" @click="load(true)">加载更早分片</button>
    </details>
    <LiveChat :session-id="live.session_id" />
  </section>
</template>
<style scoped>
.live-recording {
  margin: 16px 0;
  padding: 12px;
  border: 1px solid #dce4ee;
  border-radius: 8px;
}
ul {
  max-height: 280px;
  overflow: auto;
  padding-left: 18px;
}
p,
li {
  font-size: 0.75rem;
}
button {
  padding: 7px 12px;
  background: #e9f1ff;
  color: #245fc8;
  border: 1px solid #bdcfea;
  border-radius: 6px;
}
li {
  margin: 6px 0;
}
</style>
