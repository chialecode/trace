<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import DeliveriesPanel from "../components/DeliveriesPanel.vue";
import { authLabel } from "../lib/acquisition";
import {
  api,
  type CredentialDto,
  type ProfileDownloadReport,
  type ProfileItem,
  type ProfilePage,
} from "../lib/api";
import { displayText } from "../lib/safe";
import ProfileItems from "./profile/ProfileItems.vue";
import "../styles/pages.css";

const link = ref("");
const credentialId = ref("");
const credentials = ref<CredentialDto[]>([]);
const page = ref<ProfilePage | null>(null);
const items = ref<ProfileItem[]>([]);
const picked = reactive(new Set<string>());
const loading = ref(false);
const error = ref("");
const targetKind = ref<"library" | "folder">("library");
const folder = ref("");
const defaultFolder = ref("");
const submitting = ref(false);
const report = ref<ProfileDownloadReport | null>(null);
const submitError = ref("");
const refreshKey = ref(0);
let request = 0;
let alive = true;

const platformCredentials = computed(() =>
  credentials.value.filter((c) => !page.value || c.platform.split(":")[0] === page.value.platform),
);
const pickable = computed(() => items.value.filter((i) => !i.in_library));

function reset(next: ProfilePage | null) {
  page.value = next;
  items.value = next ? [...next.items] : [];
  picked.clear();
  report.value = null;
  submitError.value = "";
}
async function load(args: {
  url?: string;
  previewId?: string;
  ruleId?: string;
}): Promise<ProfilePage | null> {
  const current = ++request;
  loading.value = true;
  error.value = "";
  try {
    const next = await api.profilePreview({
      ...args,
      credentialId: credentialId.value || undefined,
    });
    return alive && current === request ? next : null;
  } catch (e) {
    if (alive && current === request) error.value = String(e);
    return null;
  } finally {
    if (alive && current === request) loading.value = false;
  }
}
async function start(ruleId?: string) {
  const url = link.value.trim();
  if (!url || loading.value) return;
  const next = await load({ url, ruleId });
  if (next) reset(next);
}
async function more() {
  if (!page.value || loading.value) return;
  const next = await load({ previewId: page.value.preview_id });
  if (!next) return;
  const known = new Set(items.value.map((i) => i.native_key));
  items.value = [...items.value, ...next.items.filter((i) => !known.has(i.native_key))];
  page.value = { ...next, items: [] };
}
function toggle(key: string, on: boolean) {
  if (on) picked.add(key);
  else picked.delete(key);
}
function range(keys: string[], on: boolean) {
  keys.forEach((key) => toggle(key, on));
}
const selectNew = () => pickable.value.forEach((i) => picked.add(i.native_key));
const onEnter = (event: KeyboardEvent) => {
  if (event.isComposing || event.keyCode === 229) return;
  void start();
};

async function submit() {
  if (!page.value || !picked.size || submitting.value) return;
  if (targetKind.value === "folder" && !folder.value.trim() && !defaultFolder.value) {
    submitError.value = "请填写下载文件夹，或先在设置里指定默认下载文件夹。";
    return;
  }
  submitting.value = true;
  submitError.value = "";
  try {
    const target =
      targetKind.value === "folder"
        ? { kind: "folder", dir: folder.value.trim() || null }
        : { kind: "library", dir: null };
    report.value = await api.profileDownload(page.value.preview_id, [...picked], target);
    picked.clear();
    refreshKey.value++;
  } catch (e) {
    submitError.value = String(e);
  } finally {
    submitting.value = false;
  }
}
const skippedReason = (reason: string) => displayText(reason);

onMounted(async () => {
  const [creds, settings] = await Promise.all([
    api.credentialList().catch(() => ({ items: [] as CredentialDto[] })),
    api.downloadSettings().catch(() => null),
  ]);
  if (!alive) return;
  credentials.value = creds.items.filter((c) => c.status === "valid");
  defaultFolder.value = settings?.default_dir ?? "";
});
onBeforeUnmount(() => {
  alive = false;
  ++request;
});
</script>

<template>
  <div class="page-stack">
    <header>
      <h2>主页预览与勾选下载</h2>
      <p class="page-note">
        粘贴任一网站的作者主页链接，先看内容列表，再勾选想要的几条下载。勾选可以跨页保留；已在资源库的内容会标出，不会重复保存。
      </p>
    </header>

    <section class="panel" aria-label="作者主页">
      <div class="row-line">
        <label class="field grow">
          <span>作者主页链接</span>
          <input
            v-model="link"
            autocomplete="off"
            inputmode="url"
            placeholder="粘贴微博、小红书、Instagram、X、YouTube、抖音、TikTok、哔哩哔哩的主页链接"
            aria-label="作者主页链接"
            @keydown.enter="onEnter"
          />
        </label>
        <label class="field">
          <span>访问身份</span>
          <select v-model="credentialId" aria-label="访问身份">
            <option value="">自动（已有凭证或访客）</option>
            <option
              v-for="c in platformCredentials"
              :key="c.credential_id"
              :value="c.credential_id"
            >
              {{ displayText(c.subject || c.credential_id) }}
            </option>
          </select>
        </label>
        <button class="tool-button primary" :disabled="loading || !link.trim()" @click="start()">
          {{ loading && !page ? "读取中…" : "预览主页" }}
        </button>
      </div>
      <div v-if="error" class="state-box" role="alert">
        <span>{{ error }}</span>
        <span class="page-note"
          >遇到验证码、限流或需要登录时 TRACE
          会停下，不会换个方式硬闯；请稍后再试或到“账号与登录”处理。</span
        >
      </div>
    </section>

    <section v-if="page" class="panel" aria-label="预览结果">
      <header class="row-line">
        <h3 class="untrusted-text">
          {{ displayText(page.account_name) || displayText(page.account_key) }}
        </h3>
        <span class="badge">{{ page.site_title }}</span>
        <span class="badge">{{ authLabel(page.authentication) }}</span>
        <label v-if="page.alternatives.length" class="field">
          <span>换一种读取方式</span>
          <select
            :value="page.rule_id"
            aria-label="换一种读取方式"
            @change="start(($event.target as HTMLSelectElement).value)"
          >
            <option :value="page.rule_id">{{ page.rule_title }}</option>
            <option v-for="a in page.alternatives" :key="a.rule_id" :value="a.rule_id">
              {{ a.title }}
            </option>
          </select>
        </label>
      </header>
      <p class="page-note" role="status">
        已读取 {{ page.pages_loaded }} 页（最多 {{ page.page_limit }} 页），共
        {{ items.length }} 条；已选 {{ picked.size }} 条。
        <template v-if="page.limited"
          >已到读取上限，这个主页还有更多内容，可以用订阅分批回填。</template
        >
      </p>
      <div class="row-line">
        <button class="tool-button" :disabled="!pickable.length" @click="selectNew">
          选中已读取的未保存内容
        </button>
        <button class="tool-button" :disabled="!picked.size" @click="picked.clear()">
          清空选择
        </button>
      </div>
      <p v-if="!items.length" class="state-box">
        这个主页下没有读到内容（可能为空、需要登录或暂时不可见）。
      </p>
      <ProfileItems v-else :items="items" :selected="picked" @toggle="toggle" @range="range" />
      <div class="row-line">
        <button v-if="page.has_more" class="tool-button" :disabled="loading" @click="more">
          {{ loading ? "读取中…" : "加载更多" }}
        </button>
        <span v-else class="page-note">已经读到末尾。</span>
      </div>

      <fieldset class="target">
        <legend>下载到哪里</legend>
        <label><input v-model="targetKind" type="radio" value="library" />仅放进资源库</label>
        <label
          ><input v-model="targetKind" type="radio" value="folder" />资源库 +
          同时写一份到下载文件夹</label
        >
        <label v-if="targetKind === 'folder'" class="field">
          <span>下载文件夹（留空用默认：{{ defaultFolder || "尚未设置" }}）</span>
          <input v-model="folder" autocomplete="off" aria-label="下载文件夹" />
        </label>
      </fieldset>
      <div class="row-line">
        <button class="tool-button primary" :disabled="!picked.size || submitting" @click="submit">
          {{ submitting ? "提交中…" : `下载所选 ${picked.size} 条` }}
        </button>
      </div>
      <p v-if="submitError" class="state-box" role="alert">{{ submitError }}</p>
      <div v-if="report" class="state-box" role="status">
        已接收 {{ report.accepted }} 条，{{ report.already_in_library }} 条资源库里已有；
        <template v-if="report.skipped.length">
          跳过 {{ report.skipped.length }} 条：
          <ul>
            <li v-for="s in report.skipped" :key="s.native_key">
              <span class="untrusted-text">{{ displayText(s.native_key) }}</span
              >：{{ skippedReason(s.reason) }}
            </li>
          </ul>
        </template>
        进度见下方“最近的下载”。
      </div>
    </section>

    <DeliveriesPanel :refresh-key="refreshKey" />
  </div>
</template>

<style scoped>
.panel {
  display: grid;
  gap: 10px;
  padding: 14px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #fff;
}
.target {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 8px 12px;
  border: 1px solid #e3ebf7;
  border-radius: 8px;
}
.target label {
  display: flex;
  gap: 6px;
  align-items: center;
}
.target .field {
  display: grid;
}
</style>
