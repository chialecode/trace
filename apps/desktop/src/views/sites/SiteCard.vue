<script setup lang="ts">
import { computed } from "vue";
import type { SiteView } from "../../lib/api";
import {
  contentKindLabel,
  entryLabel,
  loginLabel,
  networkLabel,
  objectLabel,
  ruleStatusLabel,
  ruleStatusTone,
  siteLabel,
} from "../../lib/sites";

const props = defineProps<{ site: SiteView }>();
const implemented = computed(
  () => props.site.rules.filter((r) => r.declaration.status === "implemented").length,
);
const name = computed(() => props.site.title || siteLabel(props.site.id));
</script>

<template>
  <section class="site-card" :aria-label="name">
    <header>
      <h3 class="untrusted-text">{{ name }}</h3>
      <span class="badge">{{ loginLabel(site.login) }}</span>
      <span class="badge">{{ networkLabel(site.network) }}</span>
      <span class="page-note">已实现 {{ implemented }} / {{ site.rules.length }} 条规则</span>
    </header>
    <p v-if="site.plugin_state !== 'enabled'" class="state-box" role="status">
      这个网站的模块当前未启用（{{ site.plugin_state }}），规则只作展示，暂不能使用。
    </p>
    <p v-if="site.login !== 'none'" class="page-note">
      {{
        site.login === "required"
          ? "该网站大多数内容需要登录。登录凭证只保存在 TRACE 的系统凭证库里，站点模块拿不到。"
          : "不登录也能取得公开内容；登录后可取得更完整的内容或历史。登录凭证只保存在 TRACE 的系统凭证库里。"
      }}
    </p>
    <div class="scroll-x">
      <table class="plain-table">
        <thead>
          <tr>
            <th>规则</th>
            <th>入口</th>
            <th>对象</th>
            <th>内容</th>
            <th>登录</th>
            <th>状态</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="rule in site.rules" :key="rule.declaration.id">
            <tr>
              <td>
                <strong class="untrusted-text">{{ rule.declaration.title }}</strong>
                <div class="page-note untrusted-text">{{ rule.declaration.description }}</div>
              </td>
              <td>{{ entryLabel(rule.declaration.entry) }}</td>
              <td>
                {{
                  objectLabel(
                    rule.declaration.url_patterns[0]?.object ??
                      (rule.declaration.entry === "link" ? "entry" : "account"),
                  )
                }}
              </td>
              <td>{{ rule.declaration.content_kinds.map(contentKindLabel).join("、") || "—" }}</td>
              <td>{{ loginLabel(rule.declaration.login) }}</td>
              <td>
                <span :class="['badge', ruleStatusTone(rule.declaration.status)]">{{
                  ruleStatusLabel(rule.declaration.status)
                }}</span>
              </td>
            </tr>
            <tr
              v-if="
                rule.declaration.limits.length ||
                rule.declaration.verified_at ||
                rule.declaration.login_note
              "
              class="detail-row"
            >
              <td colspan="6">
                <ul v-if="rule.declaration.limits.length" class="limits">
                  <li v-for="(limit, i) in rule.declaration.limits" :key="i">{{ limit }}</li>
                </ul>
                <p v-if="rule.declaration.login_note" class="page-note">
                  {{ rule.declaration.login_note }}
                </p>
                <p class="page-note">
                  {{
                    rule.declaration.verified_at
                      ? `最近一次真实验证：${rule.declaration.verified_at}（${rule.declaration.verified_scope ?? "范围未记录"}）`
                      : "尚未用真实站点验证过。"
                  }}
                </p>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.site-card {
  display: grid;
  gap: 8px;
  padding: 14px;
  border: 1px solid #dfe8f5;
  border-radius: 8px;
  background: #fff;
}
.site-card header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.site-card h3 {
  max-width: 100%;
}
.detail-row td {
  background: #f8fbff;
}
.limits {
  margin: 0 0 4px;
  padding-left: 18px;
  color: #8a5a00;
  font-size: 0.8125rem;
}
</style>
