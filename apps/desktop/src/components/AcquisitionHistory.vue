<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from "vue";
import { api, type AcquisitionDto } from "../lib/api";
import { authLabel, actualSpec } from "../lib/acquisition";
import { formatTime } from "../lib/format";
const props = defineProps<{ entryId: string }>();
const items = ref<AcquisitionDto[]>([]),
  busy = ref(false),
  error = ref(""),
  next = ref<number>();
let generation = 0;
const outcomes: Record<string, string> = {
  observed: "详情观察",
  acquired: "已取得资源",
  cache_verified: "已验证本地副本",
  legacy: "旧数据，认证依据缺失",
  authentication_unavailable: "认证未能验证",
};
async function load(older = false) {
  const run = ++generation;
  busy.value = true;
  error.value = "";
  try {
    const page = await api.archiveAcquisitions(props.entryId, older ? next.value : undefined);
    if (run !== generation) return;
    items.value = older ? [...items.value, ...page.items] : page.items;
    next.value = page.next_before ?? undefined;
  } catch {
    if (run === generation) error.value = "获取历史暂时无法读取，请重试。";
  } finally {
    if (run === generation) busy.value = false;
  }
}
watch(
  () => props.entryId,
  () => {
    generation++;
    items.value = [];
    next.value = undefined;
    busy.value = false;
  },
);
onBeforeUnmount(() => generation++);
</script>
<template>
  <details
    class="acquisition-history"
    @toggle="($event.currentTarget as HTMLDetailsElement).open && !items.length && load()"
  >
    <summary>获取身份与历史</summary>
    <p>每次获取的身份独立保存。当前登录状态不会改变历史记录；同一内容可能出现在两条时间线中。</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <button :disabled="busy" @click="load()">刷新历史</button>
    <ul>
      <li v-for="item in items" :key="item.acquisition_id">
        <strong>{{ authLabel(item.auth_state) }}</strong> ·
        {{ outcomes[item.outcome] ?? "获取观察" }}
        <p>{{ formatTime(item.started_at) }} ～ {{ formatTime(item.finished_at) }}</p>
        <p v-if="item.variant_id">{{ actualSpec(item.measured_spec) }}</p>
        <p v-if="item.validated_at">
          当次验证 {{ formatTime(item.validated_at) }} ·
          {{ item.current_credential_available ? "当前凭证仍可用" : "当前凭证不可用或已更换" }}
        </p>
      </li>
    </ul>
    <p v-if="!busy && !items.length">暂无获取记录，身份为未知。</p>
    <button v-if="next !== undefined" :disabled="busy" @click="load(true)">加载更早获取</button
    ><router-link to="/upgrades">查看资源升级</router-link>
  </details>
</template>
<style scoped>
.acquisition-history {
  margin: 16px 0;
  padding: 12px;
  border: 1px solid #e6eefb;
  border-radius: 8px;
  font-size: 0.75rem;
}
.acquisition-history p {
  line-height: 1.6;
}
ul {
  padding-left: 18px;
  max-height: 320px;
  overflow: auto;
}
li {
  margin: 12px 0;
}
button,
a {
  margin: 6px 12px 6px 0;
  color: #245fc8;
}
summary {
  cursor: pointer;
  font-weight: 600;
}
</style>
