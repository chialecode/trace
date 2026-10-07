<script setup lang="ts">
import { onMounted, ref } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { h } from "vue";
import { api, type CredentialDto } from "../lib/api";
import { formatTime } from "../lib/format";

const message = useMessage();
const items = ref<CredentialDto[]>([]);
const loading = ref(false);
const showImport = ref(false);
const importPlatform = ref("bilibili");
const importSubject = ref("");
const importSecret = ref("");

const platforms = [{ label: "哔哩哔哩（bilibili）", value: "bilibili" }];

async function refresh() {
  loading.value = true;
  try {
    items.value = (await api.credentialList()).items;
  } catch (e) {
    message.error(String(e));
  } finally {
    loading.value = false;
  }
}

async function importCred() {
  if (!importSecret.value.trim()) {
    message.warning("请粘贴 Cookie 内容（仅保存在本机加密 vault）");
    return;
  }
  try {
    const r = await api.credentialImport(
      importPlatform.value,
      importSubject.value || null,
      importSecret.value,
    );
    message.success(`已导入（${r.credential_id}），状态：${r.status}`);
    showImport.value = false;
    importSecret.value = "";
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

async function validate(credentialId: string) {
  try {
    const r = await api.credentialValidate(credentialId);
    message.success(`验证结果：${r.status}`);
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

async function revoke(credentialId: string) {
  try {
    await api.credentialRevoke(credentialId);
    message.success("已撤销：删除本机加密文件；不影响已保存档案");
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

const columns: DataTableColumns<CredentialDto> = [
  { title: "平台", key: "platform", width: 110 },
  { title: "主体", key: "subject", width: 160, render: (r) => r.subject ?? "-" },
  {
    title: "状态",
    key: "status",
    width: 110,
    render: (r) => {
      const type =
        r.status === "valid" || r.status === "authenticated"
          ? "success"
          : r.status === "invalid"
            ? "error"
            : "warning";
      return h(NTag, { type, size: "small" }, { default: () => r.status });
    },
  },
  { title: "创建时间", key: "created_at", width: 170, render: (r) => formatTime(r.created_at) },
  {
    title: "",
    key: "actions",
    width: 160,
    render: (r) =>
      h("div", { class: "row-actions" }, [
        h(NButton, { size: "small", onClick: () => validate(r.credential_id) }, { default: () => "验证" }),
        h(
          NButton,
          { size: "small", quaternary: true, type: "error", onClick: () => revoke(r.credential_id) },
          { default: () => "撤销" },
        ),
      ]),
  },
];

onMounted(refresh);
</script>

<template>
  <n-card title="账号与登录">
    <n-alert type="info" :bordered="false" style="margin-bottom: 12px">
      凭证只保存在本机用户加密目录（DPAPI），不进入档案、备份、导出或日志；恢复后需要重新登录。
      现场登录与验证需用户自行完成授权。
    </n-alert>
    <template #header-extra>
      <n-button size="small" type="primary" @click="showImport = true">导入 Cookie</n-button>
    </template>
    <n-data-table :columns="columns" :data="items" :loading="loading" size="small" />

    <n-modal v-model:show="showImport" preset="card" title="导入 Cookie" style="width: 520px">
      <n-form>
        <n-form-item label="平台">
          <n-select v-model:value="importPlatform" :options="platforms" />
        </n-form-item>
        <n-form-item label="主体（可选，如 UP 主名）">
          <n-input v-model:value="importSubject" />
        </n-form-item>
        <n-form-item label="Cookie 文本">
          <n-input
            v-model:value="importSecret"
            type="textarea"
            :rows="5"
            placeholder="粘贴浏览器导出的 Cookie 字符串；提交后立即加密保存，聊天与日志不回显"
          />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-button type="primary" @click="importCred">导入并验证</n-button>
      </template>
    </n-modal>
  </n-card>
</template>

<style scoped>
.row-actions {
  display: flex;
  gap: 4px;
}
</style>
