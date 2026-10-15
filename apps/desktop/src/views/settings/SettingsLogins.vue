<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, type CredentialDto, type SiteView } from "../../lib/api";
import { loginLabel, SITES, siteLabel } from "../../lib/sites";

const sites = ref<SiteView[]>([]);
const credentials = ref<CredentialDto[]>([]);
const error = ref("");
const loaded = ref(false);

const STATUS: Record<string, string> = {
  valid: "有效",
  expired: "已失效",
  unverified: "待验证",
  revoked: "已撤销",
};

async function load() {
  error.value = "";
  try {
    const [catalog, list] = await Promise.all([
      api.siteCatalog(),
      api.credentialList().catch(() => ({ items: [] as CredentialDto[] })),
    ]);
    sites.value = [...catalog.sites].sort(
      (a, b) => SITES.findIndex((s) => s.id === a.id) - SITES.findIndex((s) => s.id === b.id),
    );
    credentials.value = list.items;
  } catch (e) {
    error.value = `无法读取站点登录情况：${String(e)}`;
  } finally {
    loaded.value = true;
  }
}
const forSite = (id: string) => credentials.value.filter((c) => c.platform.split(":")[0] === id);
onMounted(load);
</script>

<template>
  <section class="settings-section" aria-labelledby="set-logins">
    <h3 id="set-logins">站点登录与风险提示</h3>
    <ul class="risk" aria-label="风险提示">
      <li>登录凭证相当于你的账号权限，只保存在本机的系统凭证库里，站点模块和界面都读不到它。</li>
      <li>建议使用专门用于归档的账号，不要导入常用的主账号；大量访问可能触发网站的风控。</li>
      <li>遇到验证码、限流或登录失效时，TRACE 会停下并如实标注，不会当作“没有更新”或“已删除”。</li>
      <li>导入、验证和撤销凭证在“账号与登录”页完成。</li>
    </ul>
    <p v-if="error" class="state-box" role="alert">
      {{ error }} <button class="tool-button" @click="load">重试</button>
    </p>
    <div v-else-if="loaded" class="scroll-x">
      <table class="plain-table">
        <thead>
          <tr>
            <th>网站</th>
            <th>是否需要登录</th>
            <th>已导入的凭证</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="site in sites" :key="site.id">
            <td>{{ site.title || siteLabel(site.id) }}</td>
            <td>{{ loginLabel(site.login) }}</td>
            <td>
              <span v-if="!forSite(site.id).length" class="page-note">
                {{ site.login === "required" ? "尚未导入，登录后才能使用" : "未登录（访客访问）" }}
              </span>
              <span v-for="c in forSite(site.id)" :key="c.credential_id" class="cred">
                <span class="untrusted-text">{{ c.subject || c.credential_id }}</span>
                <span :class="['badge', c.status === 'valid' ? 'success' : 'warning']">{{
                  STATUS[c.status] ?? c.status
                }}</span>
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <router-link to="/accounts" class="tool-button link-button">打开“账号与登录”</router-link>
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
.risk {
  margin: 0;
  padding-left: 18px;
  color: #51627b;
  font-size: 0.8125rem;
  line-height: 1.7;
}
.cred {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  margin-right: 10px;
}
.link-button {
  justify-self: start;
  display: inline-flex;
  align-items: center;
  text-decoration: none;
  color: inherit;
}
</style>
