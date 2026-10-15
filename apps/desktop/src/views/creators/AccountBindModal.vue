<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { NButton, NForm, NFormItem, NInput, NModal, NSelect, useMessage } from "naive-ui";
import { api, type CreatorDto, type Recognition } from "../../lib/api";
import { objectLabel, SITES } from "../../lib/sites";

const props = defineProps<{ creator: CreatorDto | null }>();
const emit = defineEmits<{ close: []; bound: [] }>();
const message = useMessage();
const bindNamespace = ref("bilibili");
const bindKey = ref("");
const bindCredential = ref<string | null>(null);
const bindCredentials = ref<{ label: string; value: string }[]>([]);
const binding = ref(false);

const namespaces = SITES.map((s) => ({ label: `${s.label}（${s.id}）`, value: s.id }));
const recognized = ref<Recognition | null>(null);
const recognizeError = ref("");
let recognizeTimer: ReturnType<typeof setTimeout> | undefined;
let recognizeSeq = 0;

/** A pasted link picks the site by itself; the user can still change it afterwards. */
function recognizeLink(value: string) {
  clearTimeout(recognizeTimer);
  const seq = ++recognizeSeq;
  recognized.value = null;
  recognizeError.value = "";
  if (!/^https?:\/\//i.test(value.trim())) return;
  recognizeTimer = setTimeout(async () => {
    try {
      const found = await api.siteRecognize(value.trim());
      if (seq !== recognizeSeq) return;
      recognized.value = found;
      if (found.site && namespaces.some((n) => n.value === found.site))
        bindNamespace.value = found.site;
    } catch (e) {
      if (seq === recognizeSeq) recognizeError.value = String(e);
    }
  }, 350);
}
watch(bindKey, recognizeLink);
onBeforeUnmount(() => {
  clearTimeout(recognizeTimer);
  ++recognizeSeq;
});

watch(
  () => props.creator,
  (c) => {
    if (c) bindKey.value = "";
  },
);
watch(
  bindNamespace,
  async (namespace) => {
    bindCredential.value = null;
    const rows = await api.credentialList(namespace).catch(() => ({ items: [] }));
    if (namespace === bindNamespace.value)
      bindCredentials.value = rows.items
        .filter((c) => c.status === "valid")
        .map((c) => ({ label: c.subject || c.credential_id, value: c.credential_id }));
  },
  { immediate: true },
);

async function bind() {
  if (!props.creator || !bindKey.value.trim()) {
    message.warning("请填写平台账号 ID（如 mid）");
    return;
  }
  try {
    binding.value = true;
    const creator = props.creator.creator_id,
      namespace = bindNamespace.value;
    const source = await api.accountResolve(
      namespace,
      bindKey.value.trim(),
      bindCredential.value ?? undefined,
    );
    await api.accountBind(creator, namespace, source.native_account_key);
    message.success("已绑定平台账号");
    emit("bound");
  } catch (e) {
    message.error(String(e));
  }
  binding.value = false;
}
</script>

<template>
  <n-modal
    :show="creator !== null"
    preset="card"
    title="绑定平台账号"
    style="width: min(480px, 94vw)"
    @update:show="
      (v: boolean) => {
        if (!v) emit('close');
      }
    "
  >
    <n-form>
      <n-form-item label="平台">
        <n-select v-model:value="bindNamespace" :options="namespaces" />
      </n-form-item>
      <n-form-item label="平台账号 ID 或主页链接">
        <n-input
          v-model:value="bindKey"
          :disabled="binding"
          placeholder="粘贴任一网站的作者主页链接（微博、小红书、Instagram、X、YouTube、抖音、TikTok、哔哩哔哩），或平台账号 ID"
        />
      </n-form-item>
      <p v-if="recognized?.site" class="recognized" role="status">
        已识别：{{ recognized.site_title }} ·
        {{ recognized.object ? objectLabel(recognized.object) : "无法判断对象" }}
        <template v-if="recognized.object === 'entry'"
          >——这是一条内容的链接，不是作者主页，绑定账号请换成主页链接。</template
        >
        <template v-else-if="recognized.needs_expansion">——短链接，绑定时会先展开确认。</template>
      </p>
      <p v-else-if="recognized" class="recognized" role="status">
        {{ recognized.message ?? "这个链接不属于已支持的网站，请手动选择平台并填写账号 ID。" }}
      </p>
      <p v-if="recognizeError" class="recognized" role="alert">{{ recognizeError }}</p>
      <n-form-item label="访问凭证">
        <n-select
          v-model:value="bindCredential"
          clearable
          :options="bindCredentials"
          placeholder="访客访问"
          :disabled="binding"
        />
      </n-form-item>
    </n-form>
    <template #footer>
      <n-button type="primary" :loading="binding" @click="bind">解析并绑定</n-button>
    </template>
  </n-modal>
</template>

<style scoped>
.recognized {
  margin: 0 0 12px;
  color: #51627b;
  font-size: 0.8125rem;
}
</style>
