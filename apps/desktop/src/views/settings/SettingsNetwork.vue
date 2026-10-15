<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { api, type NetworkSite, type NetworkView } from "../../lib/api";
import { networkLabel } from "../../lib/sites";

const view = ref<NetworkView | null>(null);
const error = ref("");
const note = ref("");
const busy = ref(false);

interface Draft {
  mode: string;
  proxy: string;
  username: string;
  password: string;
}
const drafts = reactive<Record<string, Draft>>({});

const MODES = [
  { value: "default", label: "跟随默认" },
  { value: "direct", label: "直连" },
  { value: "system", label: "跟随系统代理" },
  { value: "manual", label: "手动代理" },
];
const SOURCE: Record<string, string> = {
  site: "该网站单独设置",
  global: "全局设置",
  default: "默认",
};
const scopes = computed(() => [
  { scope: "global", title: "全部网站（全局）" },
  ...(view.value?.sites ?? []).map((s) => ({ scope: s.site, title: s.title })),
]);
const siteByScope = (scope: string): NetworkSite | undefined =>
  view.value?.sites.find((s) => s.site === scope);

function draftOf(scope: string): Draft {
  return (drafts[scope] ??= { mode: "default", proxy: "", username: "", password: "" });
}
function fill(next: NetworkView) {
  view.value = next;
  for (const { scope } of scopes.value) {
    const saved = next.settings.find((s) => s.scope === scope);
    Object.assign(draftOf(scope), {
      mode: saved?.mode ?? "default",
      proxy: saved?.proxy_url ?? "",
      username: "",
      password: "",
    });
  }
}
async function load() {
  try {
    fill(await api.networkGet());
    error.value = "";
  } catch (e) {
    error.value = `无法读取代理设置：${String(e)}`;
  }
}
const hasLogin = (scope: string) =>
  view.value?.settings.find((s) => s.scope === scope)?.has_login ?? false;

async function save(scope: string) {
  const draft = draftOf(scope);
  if (busy.value) return;
  if (draft.mode === "manual" && !draft.proxy.trim()) {
    note.value = "手动代理需要填写地址，例如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080。";
    return;
  }
  if (draft.mode === "manual" && Boolean(draft.username) !== Boolean(draft.password)) {
    note.value = "代理用户名和密码需要一起填写，或都留空。";
    return;
  }
  busy.value = true;
  note.value = "";
  try {
    const manual = draft.mode === "manual";
    fill(
      await api.networkSet({
        scope,
        mode: draft.mode,
        proxy_url: manual ? draft.proxy.trim() : null,
        username: manual && draft.username ? draft.username : null,
        password: manual && draft.password ? draft.password : null,
        keep_login: manual && !draft.username && !draft.password,
      }),
    );
    note.value = "代理设置已保存，之后的请求按新设置发出。";
  } catch (e) {
    note.value = String(e);
  } finally {
    draft.password = "";
    busy.value = false;
  }
}
onMounted(load);
</script>

<template>
  <section class="settings-section" aria-labelledby="set-network">
    <h3 id="set-network">网络与代理</h3>
    <p class="page-note">
      国内网站默认直连；YouTube、X、Instagram、TikTok 默认跟随系统代理（开启了 TUN
      模式时直连即可）。TRACE 只对网站请求使用代理，不会改动系统设置。
    </p>
    <p v-if="error" class="state-box" role="alert">
      {{ error }} <button class="tool-button" @click="load">重试</button>
    </p>
    <template v-if="view">
      <p class="page-note" role="status">
        系统代理：{{
          view.system.enabled
            ? `已开启（${view.system.address ?? "地址未知"}）`
            : "未开启，或使用 TUN 等不经系统代理的方式"
        }}
      </p>
      <div class="scroll-x">
        <table class="plain-table">
          <thead>
            <tr>
              <th>范围</th>
              <th>现在怎么连</th>
              <th>设置</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in scopes" :key="item.scope">
              <td>
                <strong class="untrusted-text">{{ item.title }}</strong>
                <div v-if="siteByScope(item.scope)" class="page-note">
                  {{ networkLabel(siteByScope(item.scope)!.advice) }}
                </div>
              </td>
              <td>
                <template v-if="siteByScope(item.scope)">
                  <span
                    :class="[
                      'badge',
                      siteByScope(item.scope)!.route === 'proxy' ? 'warning' : 'success',
                    ]"
                    >{{ siteByScope(item.scope)!.route === "proxy" ? "走代理" : "直连" }}</span
                  >
                  <div class="page-note">
                    {{ SOURCE[siteByScope(item.scope)!.source] ?? siteByScope(item.scope)!.source }}
                    · {{ siteByScope(item.scope)!.description }}
                  </div>
                </template>
                <span v-else class="page-note">未单独设置的网站都按这里</span>
              </td>
              <td>
                <div class="row-line">
                  <select
                    v-model="draftOf(item.scope).mode"
                    :aria-label="`${item.title}的连接方式`"
                  >
                    <option v-for="m in MODES" :key="m.value" :value="m.value">
                      {{ m.label }}
                    </option>
                  </select>
                  <template v-if="draftOf(item.scope).mode === 'manual'">
                    <input
                      v-model="draftOf(item.scope).proxy"
                      autocomplete="off"
                      placeholder="http://127.0.0.1:7890"
                      :aria-label="`${item.title}的代理地址`"
                    />
                    <input
                      v-model="draftOf(item.scope).username"
                      autocomplete="off"
                      :placeholder="
                        hasLogin(item.scope) ? '已保存用户名（留空保持）' : '用户名（可选）'
                      "
                      :aria-label="`${item.title}的代理用户名`"
                    />
                    <input
                      v-model="draftOf(item.scope).password"
                      type="password"
                      autocomplete="new-password"
                      :placeholder="
                        hasLogin(item.scope) ? '已保存密码（留空保持）' : '密码（可选）'
                      "
                      :aria-label="`${item.title}的代理密码`"
                    />
                  </template>
                  <button
                    class="tool-button"
                    :disabled="busy"
                    :aria-label="`保存${item.title}的连接方式`"
                    @click="save(item.scope)"
                  >
                    保存
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="page-note">代理的用户名和密码保存在系统凭证库里，界面不会再显示它们。</p>
    </template>
    <p v-if="note" class="page-note" role="status">{{ note }}</p>
  </section>
</template>

<style scoped>
.settings-section {
  display: grid;
  gap: 8px;
  padding: 14px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #fff;
}
.settings-section p {
  margin: 0;
}
select,
input {
  min-height: 32px;
  min-width: 0;
  padding: 0 8px;
  border: 1px solid #cdd8ea;
  border-radius: 6px;
  background: #fff;
  font: inherit;
}
</style>
