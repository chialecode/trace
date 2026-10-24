<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { api, type SiteView } from "../lib/api";
import { SITES } from "../lib/sites";
import LinkRecognizer from "./sites/LinkRecognizer.vue";
import SiteCard from "./sites/SiteCard.vue";
import "../styles/pages.css";

const sites = ref<SiteView[]>([]);
const loading = ref(true);
const error = ref("");
let alive = true;

function order(a: SiteView, b: SiteView) {
  const rank = (id: string) => {
    const at = SITES.findIndex((s) => s.id === id);
    return at < 0 ? SITES.length : at;
  };
  return rank(a.id) - rank(b.id) || a.id.localeCompare(b.id);
}

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const catalog = await api.siteCatalog();
    if (alive) sites.value = [...catalog.sites].sort(order);
  } catch (e) {
    if (alive) error.value = String(e);
  } finally {
    if (alive) loading.value = false;
  }
}
onMounted(load);
onBeforeUnmount(() => {
  alive = false;
});
</script>

<template>
  <div class="page-stack">
    <header>
      <h2>支持的网站</h2>
      <p class="page-note">
        这里列出每个网站能做什么、需不需要登录、国内能否直连，以及已知的限制。内容来自本机已安装的站点模块，不联网；“已实现”是指最近一次真实验证通过的能力，其余按实际标注，不会把没做的说成已完成。
      </p>
    </header>
    <LinkRecognizer />
    <p v-if="loading" class="state-box" role="status">正在读取站点规则…</p>
    <div v-else-if="error" class="state-box" role="alert">
      <span>无法读取站点规则：{{ error }}</span>
      <button class="tool-button" @click="load">重试</button>
    </div>
    <p v-else-if="!sites.length" class="state-box">
      还没有安装任何站点模块。到“平台管理”启用模块后，这里会列出它们的规则。
    </p>
    <SiteCard v-for="site in sites" :key="site.id" :site="site" />
  </div>
</template>
