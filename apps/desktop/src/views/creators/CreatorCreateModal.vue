<script setup lang="ts">
import { ref } from "vue";
import { NButton, NForm, NFormItem, NInput, NModal, useMessage } from "naive-ui";
import { api } from "../../lib/api";

const show = defineModel<boolean>("show", { required: true });
const emit = defineEmits<{ created: [] }>();
const message = useMessage();
const newDisplayName = ref("");
const newNotes = ref("");

async function create() {
  if (!newDisplayName.value.trim()) {
    message.warning("请填写展示名称");
    return;
  }
  try {
    await api.creatorCreate(newDisplayName.value.trim(), newNotes.value || undefined);
    message.success("已创建创作者");
    show.value = false;
    newDisplayName.value = "";
    newNotes.value = "";
    emit("created");
  } catch (e) {
    message.error(String(e));
  }
}
</script>

<template>
  <n-modal v-model:show="show" preset="card" title="新建创作者" style="width: min(480px, 94vw)">
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
</template>
