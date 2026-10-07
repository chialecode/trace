<script setup lang="ts">
import { onMounted, ref } from "vue";
import {
  NButton,
  NCard,
  NDataTable,
  NDivider,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NTabPane,
  NTabs,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { h } from "vue";
import {
  api,
  type AccountDto,
  type CreatorDto,
  type SubscriptionDto,
} from "../lib/api";
import { platformOf } from "../lib/format";

const message = useMessage();
const creators = ref<CreatorDto[]>([]);
const loading = ref(false);

// 新建创作者
const showCreate = ref(false);
const newDisplayName = ref("");
const newNotes = ref("");

// 绑定平台账号（针对选定创作者）
const bindCreator = ref<CreatorDto | null>(null);
const bindNamespace = ref("bilibili");
const bindKey = ref("");

// 订阅（针对选定账号）
const subAccount = ref<AccountDto | null>(null);
const subRangeKind = ref<"from_now" | "recent_days">("recent_days");
const subDays = ref(30);
const subIntervalMin = ref(60);

const namespaces = [
  { label: "哔哩哔哩（bilibili）", value: "bilibili" },
];

async function refresh() {
  loading.value = true;
  try {
    creators.value = (await api.creatorList()).items;
  } catch (e) {
    message.error(String(e));
  } finally {
    loading.value = false;
  }
}

async function create() {
  if (!newDisplayName.value.trim()) {
    message.warning("请填写展示名称");
    return;
  }
  try {
    await api.creatorCreate(newDisplayName.value.trim(), newNotes.value || undefined);
    message.success("已创建创作者");
    showCreate.value = false;
    newDisplayName.value = "";
    newNotes.value = "";
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

async function openBind(c: CreatorDto) {
  bindCreator.value = c;
  bindKey.value = "";
}

async function bind() {
  if (!bindCreator.value || !bindKey.value.trim()) {
    message.warning("请填写平台账号 ID（如 mid）");
    return;
  }
  try {
    await api.accountBind(bindCreator.value.creator_id, bindNamespace.value, bindKey.value.trim());
    message.success("已绑定平台账号");
    bindCreator.value = null;
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

async function openSubscribe(a: AccountDto) {
  subAccount.value = a;
}

async function subscribe() {
  if (!subAccount.value) return;
  try {
    const r = await api.subscriptionCreate(
      subAccount.value.account_id,
      subRangeKind.value,
      subRangeKind.value === "recent_days" ? subDays.value : undefined,
      subIntervalMin.value ? subIntervalMin.value * 60 : undefined,
    );
    message.success(`已创建订阅，可到任务页观察首轮扫描（${r.subscription_id}）`);
    subAccount.value = null;
    refresh();
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

const creatorColumns: DataTableColumns<CreatorDto> = [
  { title: "创作者", key: "display_name" },
  { title: "ID", key: "creator_id", width: 240 },
  {
    title: "操作",
    key: "actions",
    width: 220,
    render(row) {
      return h("div", { class: "row-actions" }, [
        h(NButton, { size: "small", onClick: () => openBind(row) }, { default: () => "绑定平台账号" }),
        h(
          NButton,
          { size: "small", quaternary: true, onClick: () => showAccounts(row.creator_id) },
          { default: () => "账号/订阅" },
        ),
      ]);
    },
  },
];

const detailTab = ref<string | null>(null);
const detailCreatorId = ref<string | null>(null);
const accounts = ref<AccountDto[]>([]);
const subs = ref<SubscriptionDto[]>([]);

async function showAccounts(creatorId: string) {
  detailCreatorId.value = creatorId;
  detailTab.value = "accounts";
  try {
    accounts.value = (await api.accountList(creatorId)).items;
    subs.value = [];
  } catch (e) {
    message.error(String(e));
  }
}

async function loadSubs(accountId: string) {
  try {
    subs.value = (await api.subscriptionList(accountId)).items;
  } catch (e) {
    message.error(String(e));
  }
}

onMounted(refresh);
</script>

<template>
  <n-card title="创作者">
    <template #header-extra>
      <n-button size="small" type="primary" @click="showCreate = true">新建创作者</n-button>
    </template>
    <n-data-table
      :columns="creatorColumns"
      :data="creators"
      :loading="loading"
      :bordered="false"
      size="small"
    />

    <n-modal v-model:show="showCreate" preset="card" title="新建创作者" style="width: 480px">
      <n-form>
        <n-form-item label="展示名称">
          <n-input v-model:value="newDisplayName" placeholder="如：示例UP主" />
        </n-form-item>
        <n-form-item label="备注">
          <n-input v-model:value="newNotes" type="textarea" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-button type="primary" @click="create">创建</n-button>
      </template>
    </n-modal>

    <n-modal
      :show="bindCreator !== null"
      preset="card"
      title="绑定平台账号"
      style="width: 480px"
      @update:show="(v: boolean) => { if (!v) bindCreator = null }"
    >
      <n-form>
        <n-form-item label="平台">
          <n-select v-model:value="bindNamespace" :options="namespaces" />
        </n-form-item>
        <n-form-item label="平台账号 ID">
          <n-input v-model:value="bindKey" placeholder="bilibili mid，如 94697" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-button type="primary" @click="bind">绑定</n-button>
      </template>
    </n-modal>

    <n-modal
      :show="subAccount !== null"
      preset="card"
      title="创建订阅"
      style="width: 480px"
      @update:show="(v: boolean) => { if (!v) subAccount = null }"
    >
      <n-form>
        <n-form-item label="范围">
          <n-select
            v-model:value="subRangeKind"
            :options="[
              { label: '仅今后新增', value: 'from_now' },
              { label: '近期历史（有界）', value: 'recent_days' },
            ]"
          />
        </n-form-item>
        <n-form-item v-if="subRangeKind === 'recent_days'" label="回看天数">
          <n-input-number v-model:value="subDays" :min="1" :max="365" />
        </n-form-item>
        <n-form-item label="检查间隔（分钟）">
          <n-input-number v-model:value="subIntervalMin" :min="5" :max="1440" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-button type="primary" @click="subscribe">创建订阅</n-button>
      </template>
    </n-modal>

    <n-modal
      :show="detailTab !== null"
      preset="card"
      title="平台账号与订阅"
      style="width: 680px"
      @update:show="(v: boolean) => (detailTab = v ? detailTab : null)"
    >
      <n-tabs :value="detailTab ?? 'accounts'" @update:value="(k: string) => (detailTab = k)">
        <n-tab-pane name="accounts" tab="平台账号">
          <n-data-table
            :columns="[
              { title: '平台', key: 'namespace', width: 120, render: (r: AccountDto) => platformOf(r.namespace) },
              { title: '账号', key: 'native_account_key' },
              {
                title: '操作', key: 'a', width: 140,
                render: (r: AccountDto) => h(NButton, { size: 'small', onClick: () => openSubscribe(r) }, { default: () => '创建订阅' }),
              },
            ]"
            :data="accounts"
            size="small"
          />
          <n-divider />
          <div v-if="subs.length">
            <n-tag v-for="s in subs" :key="s.subscription_id" size="small" class="sub-tag">
              {{ s.subscription_id }}
            </n-tag>
          </div>
        </n-tab-pane>
        <n-tab-pane name="subs" tab="订阅">
          <n-button size="small" @click="detailCreatorId && loadSubs(accounts[0]?.account_id ?? '')">
            刷新首个账号订阅
          </n-button>
          <n-data-table
            :columns="[
              { title: '订阅', key: 'subscription_id' },
              { title: '内容', key: 'content_type', width: 90 },
              { title: '状态', key: 'state', width: 110 },
              {
                title: '操作', key: 'a', width: 140,
                render: (r: SubscriptionDto) => h(NButton, { size: 'small', onClick: () => checkNow(r.subscription_id) }, { default: () => '立即检查' }),
              },
            ]"
            :data="subs"
            size="small"
          />
        </n-tab-pane>
      </n-tabs>
    </n-modal>
  </n-card>
</template>

<style scoped>
.row-actions {
  display: flex;
  gap: 8px;
}
.sub-tag {
  margin-right: 8px;
}
</style>
