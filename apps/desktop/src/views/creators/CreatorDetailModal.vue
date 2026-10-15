<script setup lang="ts">
import { h, ref, watch } from "vue";
import {
  NButton,
  NDataTable,
  NDivider,
  NModal,
  NTabPane,
  NTabs,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import AccountHistory from "../../components/AccountHistory.vue";
import { api, type AccountDto, type SubscriptionDto } from "../../lib/api";
import { platformOf } from "../../lib/format";

const props = defineProps<{ creatorId: string | null }>();
const emit = defineEmits<{
  close: [];
  subscribe: [account: AccountDto, existing?: SubscriptionDto];
}>();
const message = useMessage();
const detailTab = ref("accounts");
const accounts = ref<AccountDto[]>([]);
const subs = ref<SubscriptionDto[]>([]);

async function showAccounts(creatorId: string) {
  detailTab.value = "accounts";
  try {
    const result = (await api.accountList(creatorId)).items;
    if (props.creatorId !== creatorId) return;
    accounts.value = result;
    await loadSubs();
  } catch (e) {
    message.error(String(e));
  }
}

async function loadSubs() {
  const id = props.creatorId;
  try {
    const pages = await Promise.all(accounts.value.map((a) => api.subscriptionList(a.account_id)));
    if (id === props.creatorId) subs.value = pages.flatMap((p) => p.items);
  } catch (e) {
    message.error(String(e));
  }
}
watch(
  () => props.creatorId,
  (id) => {
    if (id) void showAccounts(id);
  },
  { immediate: true },
);
defineExpose({ loadSubs });

async function toggleSubtitles(s: SubscriptionDto) {
  try {
    await api.subscriptionUpdate(s.subscription_id, { capture_subtitles: !s.capture_subtitles });
    await loadSubs();
  } catch {
    message.error("字幕设置未能更新，请重试。");
  }
}
async function toggleAuto(s: SubscriptionDto) {
  try {
    await api.subscriptionUpdate(s.subscription_id, { auto_download: !s.auto_download });
    await loadSubs();
  } catch {
    message.error("自动下载设置未能更新，请重试。");
  }
}
async function toggleChat(s: SubscriptionDto) {
  try {
    await api.subscriptionUpdate(s.subscription_id, { capture_chat: !s.capture_chat });
    await loadSubs();
  } catch {
    message.error("弹幕采样设置未能更新，请重试。");
  }
}
function editSubscription(s: SubscriptionDto) {
  const a = accounts.value.find((a) => a.account_id === s.account_id);
  if (a) emit("subscribe", a, s);
}
async function toggleSubscription(s: SubscriptionDto) {
  try {
    await api.subscriptionSetEnabled(s.subscription_id, s.state !== "enabled");
    await loadSubs();
  } catch (e) {
    message.error(String(e));
  }
}
async function checkNow(subscriptionId: string) {
  try {
    await api.subscriptionCheckNow(subscriptionId);
    message.success("已请求立即检查");
  } catch (e) {
    message.error(String(e));
  }
}

const accountColumns: DataTableColumns<AccountDto> = [
  { title: "平台", key: "namespace", width: 120, render: (r) => platformOf(r.namespace) },
  { title: "账号", key: "native_account_key" },
  {
    title: "操作",
    key: "a",
    width: 140,
    render: (r) =>
      h(
        NButton,
        { size: "small", onClick: () => emit("subscribe", r) },
        { default: () => "创建订阅" },
      ),
  },
];
const subColumns: DataTableColumns<SubscriptionDto> = [
  { title: "订阅", key: "subscription_id" },
  { title: "间隔（秒）", key: "check_interval_seconds" },
  { title: "内容", key: "content_type", width: 90 },
  { title: "状态", key: "state", width: 110 },
  {
    title: "新视频自动下载",
    key: "auto_download",
    render: (r) =>
      r.content_type === "live"
        ? "不适用"
        : h(
            NButton,
            { size: "small", onClick: () => toggleAuto(r) },
            { default: () => (r.auto_download ? "关闭自动下载" : "开启自动下载") },
          ),
  },
  {
    title: "外部字幕",
    key: "capture_subtitles",
    render: (r) =>
      r.content_type === "live"
        ? h(
            NButton,
            { size: "small", onClick: () => toggleSubtitles(r) },
            { default: () => (r.capture_subtitles ? "关闭字幕" : "开启字幕") },
          )
        : "不适用",
  },
  {
    title: "弹幕采样",
    key: "capture_chat",
    render: (r) =>
      r.content_type === "live"
        ? h(
            NButton,
            { size: "small", onClick: () => toggleChat(r) },
            { default: () => (r.capture_chat ? "关闭采样" : "开启采样") },
          )
        : "不适用",
  },
  {
    title: "操作",
    key: "a",
    width: 140,
    render: (r) =>
      h("div", { class: "row-actions" }, [
        h(
          NButton,
          { size: "small", onClick: () => editSubscription(r) },
          { default: () => "配置 / 覆盖" },
        ),
        h(
          NButton,
          { size: "small", onClick: () => toggleSubscription(r) },
          { default: () => (r.state === "enabled" ? "暂停" : "恢复") },
        ),
        h(
          NButton,
          { size: "small", onClick: () => checkNow(r.subscription_id) },
          { default: () => "立即检查" },
        ),
      ]),
  },
];
</script>

<template>
  <n-modal
    :show="creatorId !== null"
    preset="card"
    title="平台账号与订阅"
    style="width: min(1000px, 96vw)"
    @update:show="
      (v: boolean) => {
        if (!v) emit('close');
      }
    "
  >
    <n-tabs :value="detailTab" @update:value="(k: string) => (detailTab = k)">
      <n-tab-pane name="accounts" tab="平台账号">
        <n-data-table :columns="accountColumns" :data="accounts" size="small" />
        <n-divider />
        <AccountHistory
          v-for="account in accounts"
          :key="account.account_id"
          :account="account"
          @updated="creatorId && showAccounts(creatorId)"
        />
        <div v-if="subs.length">
          <n-tag v-for="s in subs" :key="s.subscription_id" size="small" class="sub-tag">
            {{ s.subscription_id }}
          </n-tag>
        </div>
      </n-tab-pane>
      <n-tab-pane name="subs" tab="订阅">
        <n-button size="small" @click="loadSubs()"> 刷新全部账号订阅 </n-button>
        <n-data-table :columns="subColumns" :data="subs" size="small" />
      </n-tab-pane>
    </n-tabs>
  </n-modal>
</template>

<style scoped>
.row-actions {
  flex-wrap: wrap;
  display: flex;
  gap: 8px;
}
.sub-tag {
  margin-right: 8px;
}
</style>
