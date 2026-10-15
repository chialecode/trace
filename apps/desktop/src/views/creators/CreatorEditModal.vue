<script setup lang="ts">
import { ref, watch } from "vue";
import { NButton, NForm, NFormItem, NInput, NModal, useMessage } from "naive-ui";
import { api, type CreatorDto } from "../../lib/api";

const props = defineProps<{ creator: CreatorDto | null }>();
const emit = defineEmits<{ close: []; saved: [] }>();
const message = useMessage();
const editName = ref("");
const editNotes = ref("");
const editAliases = ref("");
const editTags = ref("");

watch(
  () => props.creator,
  (c) => {
    if (!c) return;
    editName.value = c.display_name;
    editNotes.value = c.notes ?? "";
    editAliases.value = (c.aliases ?? []).join("，");
    editTags.value = (c.tags ?? []).join("，");
  },
  { immediate: true },
);

const splitLabels = (v: string) => [
  ...new Set(
    v
      .split(/[,，\n]/)
      .map((s) => s.trim())
      .filter(Boolean),
  ),
];

async function saveCreator() {
  if (!props.creator || !editName.value.trim()) return;
  try {
    await api.creatorUpdate(props.creator.creator_id, editName.value.trim(), editNotes.value, {
      aliases: splitLabels(editAliases.value),
      tags: splitLabels(editTags.value),
    });
    emit("saved");
  } catch (e) {
    message.error(String(e));
  }
}
</script>

<template>
  <n-modal
    :show="creator !== null"
    preset="card"
    title="编辑创作者"
    style="width: min(520px, 94vw)"
    @update:show="
      (v: boolean) => {
        if (!v) emit('close');
      }
    "
  >
    <n-form>
      <n-form-item label="显示名"><n-input v-model:value="editName" /></n-form-item>
      <n-form-item label="别名（逗号分隔）"><n-input v-model:value="editAliases" /></n-form-item>
      <n-form-item label="标签（逗号分隔）"><n-input v-model:value="editTags" /></n-form-item>
      <n-form-item label="备注"><n-input v-model:value="editNotes" type="textarea" /></n-form-item>
    </n-form>
    <p>归档创作者后仍保留档案与订阅；需要停止采集时请暂停订阅。</p>
    <template #footer><n-button type="primary" @click="saveCreator">保存</n-button></template>
  </n-modal>
</template>
