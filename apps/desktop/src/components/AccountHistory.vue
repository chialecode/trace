<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from "vue";
import { api, type AccountDto } from "../lib/api";
const props = defineProps<{ account: AccountDto }>();
const emit = defineEmits<{ updated: [] }>();
const items = ref<Awaited<ReturnType<typeof api.accountHistory>>["items"]>([]),
  next = ref<number | null>(),
  busy = ref(false),
  error = ref(""),
  opened = ref(false);
let generation = 0;
async function load(more = false) {
  const ticket = ++generation;
  busy.value = true;
  error.value = "";
  try {
    const page = await api.accountHistory(
      props.account.account_id,
      more ? (next.value ?? undefined) : undefined,
    );
    if (ticket !== generation) return;
    items.value = more ? [...items.value, ...page.items] : page.items;
    next.value = page.next_before;
  } catch {
    if (ticket === generation) error.value = "资料观察读取失败。";
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
async function refresh() {
  const ticket = ++generation;
  busy.value = true;
  error.value = "";
  const account = props.account;
  try {
    await api.accountResolve(account.namespace, account.native_account_key);
    if (ticket === generation) {
      emit("updated");
      await load();
    }
  } catch {
    if (ticket === generation)
      error.value =
        "来源资料未能更新；已有观察仍可离线读取。需要登录时，可在绑定账号时选择有效凭证。";
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
watch(
  () => props.account.account_id,
  () => {
    generation++;
    items.value = [];
    next.value = null;
    error.value = "";
    busy.value = false;
    if (opened.value) void load();
  },
);
onBeforeUnmount(() => generation++);
function toggle(event: Event) {
  const before = opened.value;
  opened.value = (event.currentTarget as HTMLDetailsElement).open;
  if (opened.value && !before) void load();
}
</script>
<template>
  <details @toggle="toggle">
    <summary>{{ account.display_name || account.native_account_key }} · 资料与别名观察</summary>
    <button :disabled="busy" @click="refresh">从来源更新资料</button>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="busy" role="status">正在读取…</p>
    <p v-else-if="!items.length">尚未保存账号资料观察。</p>
    <section v-for="item in items" :key="item.sequence">
      <p>{{ item.display_name || "来源未给昵称" }} · {{ item.observed_at }}</p>
      <details>
        <summary>安全元数据与身份依据</summary>
        <pre>{{ JSON.stringify(item.evidence, null, 2) }}</pre>
      </details>
    </section>
    <button v-if="next" :disabled="busy" @click="load(true)">更早资料</button>
  </details>
</template>
<style scoped>
details {
  margin: 10px 0;
}
summary {
  cursor: pointer;
}
pre {
  max-height: 320px;
  overflow: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-size: 0.75rem;
}
button {
  border: 1px solid #c8d7eb;
  background: #eef4ff;
  color: #245fc8;
  padding: 4px 8px;
  border-radius: 5px;
}
p {
  font-size: 0.75rem;
  color: #60728a;
}
</style>
