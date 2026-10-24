<script setup lang="ts">
import { ref, watch } from "vue";
import { NButton, NForm, NFormItem, NInputNumber, NModal, NSelect, useMessage } from "naive-ui";
import { api, type AccountDto, type QualityRequirement, type SubscriptionDto } from "../../lib/api";

const props = defineProps<{ account: AccountDto | null; existing: SubscriptionDto | null }>();
const emit = defineEmits<{ close: []; saved: [] }>();
const message = useMessage();

const subRangeKind = ref<"from_now" | "recent_days" | "all" | "date_range">("from_now");
const subStart = ref(""),
  subEnd = ref(""),
  subHeight = ref<number | null>(null),
  subFps = ref<number | null>(null),
  subStrict = ref(false),
  subPreferFps = ref(false),
  savingSub = ref(false);
const subDays = ref(30);
const subIntervalSeconds = ref(300);
const subLiveIntervalSeconds = ref(30);
const subTypes = ref<string[]>(["video"]);
const subSubtitles = ref(false);
const subAuto = ref(false);
const subChat = ref(false),
  subCredential = ref<string | null>(null),
  subCredentials = ref<{ label: string; value: string }[]>([]);
const contentTypes = [
  { label: "视频", value: "video" },
  { label: "动态", value: "dynamic" },
  { label: "图文", value: "image" },
  { label: "专栏 / 长文", value: "article" },
  { label: "直播录制", value: "live" },
];
const rangeOptions = [
  { label: "仅今后新增", value: "from_now" },
  { label: "近期历史（有界）", value: "recent_days" },
  { label: "全部可取得历史（分批回填）", value: "all" },
  { label: "指定起止时间", value: "date_range" },
];

function requestedQuality(): QualityRequirement {
  return {
    min_height: subHeight.value,
    min_fps: subFps.value,
    strict: subStrict.value,
    prefer_fps: subPreferFps.value,
  };
}
function utcDate(v: string): string | undefined {
  return v ? new Date(v).toISOString() : undefined;
}
function localDate(v: unknown): string {
  if (typeof v !== "string") return "";
  const d = new Date(v);
  if (!Number.isFinite(d.valueOf())) return "";
  return new Date(d.valueOf() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

async function load(a: AccountDto, existing?: SubscriptionDto) {
  const range =
    typeof existing?.range === "string"
      ? JSON.parse(existing.range)
      : (existing?.range as
          { kind?: string; days?: number; start?: string; end?: string } | undefined);
  subRangeKind.value = (range?.kind ?? "from_now") as typeof subRangeKind.value;
  subDays.value = range?.days ?? 30;
  subStart.value = localDate(range?.start);
  subEnd.value = localDate(range?.end);
  subTypes.value = existing ? [existing.content_type] : ["video"];
  subIntervalSeconds.value = existing?.check_interval_seconds ?? 300;
  subLiveIntervalSeconds.value = existing?.check_interval_seconds ?? 30;
  subHeight.value = existing?.quality?.min_height ?? null;
  subFps.value = existing?.quality?.min_fps ?? null;
  subStrict.value = existing?.quality?.strict ?? false;
  subPreferFps.value = existing?.quality?.prefer_fps ?? false;
  subCredential.value = existing?.credential_id ?? null;
  subChat.value = existing?.capture_chat ?? false;
  subSubtitles.value = existing?.capture_subtitles ?? false;
  subAuto.value = existing?.auto_download ?? false;
  subCredentials.value = [];
  const rows = await api.credentialList(a.namespace.split(":")[0]).catch(() => ({ items: [] }));
  if (props.account?.account_id === a.account_id)
    subCredentials.value = rows.items
      .filter((c) => c.status === "valid" || c.credential_id === existing?.credential_id)
      .map((c) => ({
        label: (c.subject || c.credential_id) + (c.status === "valid" ? "" : "（需验证）"),
        value: c.credential_id,
      }));
}
watch(
  () => props.account,
  (a) => {
    if (a) void load(a, props.existing ?? undefined);
  },
  { immediate: true },
);

async function subscribe() {
  if (!props.account || savingSub.value) return;
  if (subStrict.value && !subHeight.value && !subFps.value) {
    message.warning("严格模式需要最低高度或帧率。");
    return;
  }
  if (!subTypes.value.length) {
    message.warning("请至少选择一种内容。");
    return;
  }
  savingSub.value = true;
  try {
    const start = utcDate(subStart.value),
      end = utcDate(subEnd.value);
    if (
      subRangeKind.value === "date_range" &&
      ((!start && !end) || (start && end && start >= end))
    ) {
      message.warning("请填写有效的起止范围，至少一个边界。");
      return;
    }
    if (props.existing) {
      const range =
        subRangeKind.value === "recent_days"
          ? { kind: "recent_days", days: subDays.value }
          : subRangeKind.value === "date_range"
            ? { kind: "date_range", start, end }
            : { kind: subRangeKind.value };
      const r = props.existing;
      await api.subscriptionUpdate(r.subscription_id, {
        range,
        quality: requestedQuality(),
        ...(subCredential.value !== r.credential_id
          ? { credential_id: subCredential.value ?? "" }
          : {}),
        ...(subAuto.value !== (r.auto_download ?? false) ? { auto_download: subAuto.value } : {}),
        check_interval_seconds:
          r.content_type === "live" ? subLiveIntervalSeconds.value : subIntervalSeconds.value,
        ...(r.content_type === "live"
          ? { capture_chat: subChat.value, capture_subtitles: subSubtitles.value }
          : {}),
      });
    } else {
      for (const contentType of [...subTypes.value]) {
        await api.subscriptionCreate(
          props.account.account_id,
          subRangeKind.value,
          subRangeKind.value === "recent_days" ? subDays.value : undefined,
          contentType === "live" ? subLiveIntervalSeconds.value : subIntervalSeconds.value,
          contentType,
          {
            credentialId: subCredential.value ?? undefined,
            ...(subAuto.value ? { autoDownload: true } : {}),
            captureChat: contentType === "live" && subChat.value,
            captureSubtitles: contentType === "live" && subSubtitles.value,
            quality: requestedQuality(),
            start,
            end,
          },
        );
        subTypes.value = subTypes.value.filter((t) => t !== contentType);
      }
    }
    message.success("订阅配置已保存。");
    emit("saved");
  } catch (e) {
    message.error(String(e));
  } finally {
    savingSub.value = false;
  }
}
</script>

<template>
  <n-modal
    :show="account !== null"
    preset="card"
    :title="existing ? '编辑订阅' : '创建订阅'"
    style="width: min(480px, 94vw)"
    @update:show="
      (v: boolean) => {
        if (!v) emit('close');
      }
    "
  >
    <n-form>
      <p v-if="existing">
        最近覆盖记录：{{ JSON.stringify(existing.coverage ?? []) }}。来源不可见部分不计为完整历史。
      </p>
      <n-form-item label="范围">
        <n-select v-model:value="subRangeKind" :options="rangeOptions" />
      </n-form-item>
      <n-form-item v-if="subRangeKind === 'recent_days'" label="回看天数">
        <n-input-number v-model:value="subDays" :min="1" :max="365" />
      </n-form-item>
      <n-form-item v-if="subRangeKind === 'date_range'" label="起点（含） / 终点（含），本地时间">
        <div class="range-fields">
          <input v-model="subStart" type="datetime-local" aria-label="起点" />
          <input v-model="subEnd" type="datetime-local" aria-label="终点" />
        </div>
      </n-form-item>
      <n-form-item
        v-if="!existing || existing.content_type !== 'live'"
        label="普通内容检查间隔（秒）"
      >
        <n-input-number v-model:value="subIntervalSeconds" :min="5" :max="86400" />
      </n-form-item>
      <n-form-item v-if="subTypes.includes('live')" label="直播检查间隔（秒）">
        <n-input-number v-model:value="subLiveIntervalSeconds" :min="5" :max="86400" />
      </n-form-item>
      <n-form-item label="内容类型">
        <n-select
          :disabled="!!existing"
          v-model:value="subTypes"
          multiple
          :options="contentTypes"
        />
      </n-form-item>
      <n-form-item label="访问凭证">
        <n-select
          v-model:value="subCredential"
          clearable
          placeholder="访客访问"
          :options="subCredentials"
        />
      </n-form-item>
      <n-form-item label="最低视频高度（像素，可不指定）">
        <n-input-number v-model:value="subHeight" :min="1" :max="16384" />
      </n-form-item>
      <n-form-item label="最低帧率（可不指定）">
        <n-input-number v-model:value="subFps" :min="1" :max="1000" />
      </n-form-item>
      <label class="setting auto-download"
        ><input v-model="subAuto" type="checkbox" />自动下载新发现的视频</label
      >
      <p class="page-note">
        {{
          subAuto
            ? "之后检查到的新视频会自动下载；已经标为“未下载”的内容不受影响，可在时间线里手动补下载。"
            : "默认关闭：新发现的内容先保存文字、图片和视频信息，视频标为“未下载（按订阅设置）”，需要时再点“补下载”。"
        }}
      </p>
      <label class="setting"><input v-model="subPreferFps" type="checkbox" />优先帧率</label>
      <label class="setting"><input v-model="subStrict" type="checkbox" />只接受达标版本</label>
      <p>
        {{
          subStrict
            ? "实际规格不足或未知时不收录该媒体，保留未收录原因和直播缺口。"
            : "先保存当前可取得版本，记录期望与实际规格，后续补全质量。"
        }}
      </p>
      <label v-if="subTypes.includes('live')"
        ><input v-model="subChat" type="checkbox" />同时保存可取得的直播弹幕历史采样（约每 10
        秒，覆盖可能不完整）</label
      >
      <label v-if="subTypes.includes('live')"
        ><input v-model="subSubtitles" type="checkbox" />保存 HLS
        来源提供的外部字幕（若无该轨道则如实记录）</label
      >
    </n-form>
    <template #footer>
      <n-button type="primary" :loading="savingSub" @click="subscribe">保存订阅</n-button>
    </template>
  </n-modal>
</template>

<style scoped>
.setting {
  display: block;
  margin: 8px 0;
}
.range-fields {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
</style>
