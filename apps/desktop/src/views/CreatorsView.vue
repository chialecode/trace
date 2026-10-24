<script setup lang="ts">
import { h, onMounted, ref } from "vue";
import { NButton, NCard, NDataTable, useMessage, type DataTableColumns } from "naive-ui";
import { api, type AccountDto, type CreatorDto, type SubscriptionDto } from "../lib/api";
import AccountBindModal from "./creators/AccountBindModal.vue";
import CreatorCreateModal from "./creators/CreatorCreateModal.vue";
import CreatorDetailModal from "./creators/CreatorDetailModal.vue";
import CreatorEditModal from "./creators/CreatorEditModal.vue";
import SubscriptionModal from "./creators/SubscriptionModal.vue";

const message = useMessage();
const creators = ref<CreatorDto[]>([]);
const loading = ref(false);

// Which dialog is open, and for what.
const showCreate = ref(false);
const editCreator = ref<CreatorDto | null>(null);
const bindCreator = ref<CreatorDto | null>(null);
const detailCreatorId = ref<string | null>(null);
const subAccount = ref<AccountDto | null>(null);
const editingSub = ref<SubscriptionDto | null>(null);
const detail = ref<InstanceType<typeof CreatorDetailModal>>();

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

async function toggleCreator(c: CreatorDto) {
  try {
    await api.creatorUpdate(c.creator_id, undefined, undefined, {
      status: c.status === "archived" ? "active" : "archived",
    });
    await refresh();
  } catch (e) {
    message.error(String(e));
  }
}

async function creatorSaved() {
  editCreator.value = null;
  await refresh();
}
function accountBound() {
  bindCreator.value = null;
  void refresh();
}
function openSubscribe(account: AccountDto, existing?: SubscriptionDto) {
  editingSub.value = existing ?? null;
  subAccount.value = account;
}
async function subscriptionSaved() {
  subAccount.value = null;
  editingSub.value = null;
  await refresh();
  await detail.value?.loadSubs();
}

const creatorColumns: DataTableColumns<CreatorDto> = [
  { title: "创作者", key: "display_name" },
  { title: "状态", key: "status", render: (r) => (r.status === "archived" ? "已归档" : "活跃") },
  {
    title: "操作",
    key: "actions",
    width: 360,
    render(row) {
      return h("div", { class: "row-actions" }, [
        h(
          NButton,
          { size: "small", onClick: () => (editCreator.value = row) },
          { default: () => "编辑" },
        ),
        h(
          NButton,
          { size: "small", onClick: () => toggleCreator(row) },
          { default: () => (row.status === "archived" ? "恢复显示" : "归档") },
        ),
        h(
          NButton,
          { size: "small", onClick: () => (bindCreator.value = row) },
          { default: () => "绑定平台账号" },
        ),
        h(
          NButton,
          {
            size: "small",
            quaternary: true,
            onClick: () => (detailCreatorId.value = row.creator_id),
          },
          { default: () => "账号/订阅" },
        ),
      ]);
    },
  },
];

onMounted(refresh);
</script>

<template>
  <n-card title="创作者" data-page="creators">
    <template #header-extra>
      <router-link to="/management?tab=identity" class="management-link"
        >合并、拆分与身份候选</router-link
      >
      <n-button size="small" type="primary" @click="showCreate = true">新建创作者</n-button>
    </template>
    <n-data-table
      :columns="creatorColumns"
      :data="creators"
      :loading="loading"
      :bordered="false"
      size="small"
    />

    <CreatorEditModal :creator="editCreator" @close="editCreator = null" @saved="creatorSaved" />
    <CreatorCreateModal v-model:show="showCreate" @created="refresh" />
    <AccountBindModal :creator="bindCreator" @close="bindCreator = null" @bound="accountBound" />
    <SubscriptionModal
      :account="subAccount"
      :existing="editingSub"
      @close="
        subAccount = null;
        editingSub = null;
      "
      @saved="subscriptionSaved"
    />
    <CreatorDetailModal
      ref="detail"
      :creator-id="detailCreatorId"
      @close="detailCreatorId = null"
      @subscribe="openSubscribe"
    />
  </n-card>
</template>

<style scoped>
.row-actions {
  flex-wrap: wrap;
  display: flex;
  gap: 8px;
}
</style>
