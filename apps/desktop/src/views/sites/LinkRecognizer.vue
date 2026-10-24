<script setup lang="ts">
import { ref } from "vue";
import { api, type Recognition } from "../../lib/api";
import {
  entryLabel,
  loginLabel,
  networkLabel,
  objectLabel,
  ruleStatusLabel,
  ruleStatusTone,
} from "../../lib/sites";

const link = ref("");
const busy = ref(false);
const error = ref("");
const result = ref<Recognition | null>(null);
let request = 0;

async function recognize() {
  const value = link.value.trim();
  if (!value || busy.value) return;
  const current = ++request;
  busy.value = true;
  error.value = "";
  try {
    const found = await api.siteRecognize(value);
    if (current === request) result.value = found;
  } catch (e) {
    if (current === request) {
      result.value = null;
      error.value = String(e);
    }
  } finally {
    if (current === request) busy.value = false;
  }
}
/** Enter confirms the link; Enter that only confirms an input-method candidate does not. */
function onEnter(event: KeyboardEvent) {
  if (event.isComposing || event.keyCode === 229) return;
  void recognize();
}
</script>

<template>
  <section class="recognizer" aria-label="识别链接">
    <h3>粘贴一个链接，看 TRACE 能用它做什么</h3>
    <div class="row-line">
      <label class="field grow">
        <span>链接</span>
        <input
          v-model="link"
          type="url"
          inputmode="url"
          autocomplete="off"
          placeholder="例如某个作者的主页或一条内容的链接"
          aria-label="要识别的链接"
          @keydown.enter="onEnter"
        />
      </label>
      <button class="tool-button primary" :disabled="busy || !link.trim()" @click="recognize">
        {{ busy ? "识别中…" : "识别" }}
      </button>
    </div>
    <p v-if="error" class="state-box" role="alert">{{ error }}</p>
    <div v-else-if="result" class="found" role="status">
      <p v-if="!result.site" class="state-box">
        {{ result.message ?? "这个链接不属于已支持的网站。" }}
      </p>
      <template v-else>
        <p>
          <strong>{{ result.site_title }}</strong
          >：{{ result.object ? objectLabel(result.object) : "无法判断对象" }}
          <span v-if="result.needs_expansion" class="badge warning"
            >短链接，需要先展开才能确认</span
          >
        </p>
        <p v-if="result.message" class="page-note">{{ result.message }}</p>
        <ul v-if="result.rules.length" class="rule-list">
          <li v-for="rule in result.rules" :key="rule.rule_id">
            <span class="untrusted-text">{{ rule.title }}</span>
            <span class="badge">{{ entryLabel(rule.entry) }}</span>
            <span :class="['badge', ruleStatusTone(rule.status)]">{{
              ruleStatusLabel(rule.status)
            }}</span>
            <span class="badge">{{ loginLabel(rule.login) }}</span>
            <span class="badge">{{ networkLabel(rule.network) }}</span>
          </li>
        </ul>
        <p v-else class="state-box">已识别站点，但没有可用于这个链接的规则。</p>
        <p class="page-note">
          识别只依据链接的写法，不会访问网站；真正的归属由站点模块解析后确认。
        </p>
      </template>
    </div>
  </section>
</template>

<style scoped>
.recognizer {
  display: grid;
  gap: 8px;
  padding: 14px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #fff;
}
.found {
  display: grid;
  gap: 6px;
}
.found p {
  margin: 0;
}
.rule-list {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.rule-list li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
</style>
