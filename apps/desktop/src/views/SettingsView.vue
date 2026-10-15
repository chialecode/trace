<script setup lang="ts">
import SettingsDownloads from "./settings/SettingsDownloads.vue";
import SettingsDisplay from "./settings/SettingsDisplay.vue";
import SettingsLibrary from "./settings/SettingsLibrary.vue";
import SettingsLogins from "./settings/SettingsLogins.vue";
import SettingsNetwork from "./settings/SettingsNetwork.vue";
import "../styles/pages.css";

const sections = [
  { id: "set-display", label: "显示与文字" },
  { id: "set-library", label: "资源库位置" },
  { id: "set-downloads", label: "下载位置与命名" },
  { id: "set-network", label: "网络与代理" },
  { id: "set-logins", label: "站点登录" },
];
function jump(id: string) {
  document.getElementById(id)?.scrollIntoView?.({ block: "start" });
}
</script>

<template>
  <div class="page-stack" data-page="settings">
    <header>
      <h2>设置</h2>
      <nav class="row-line" aria-label="设置分区">
        <button v-for="s in sections" :key="s.id" class="tool-button" @click="jump(s.id)">
          {{ s.label }}
        </button>
        <router-link to="/storage" class="tool-button plain">存储、备份与导出</router-link>
        <router-link to="/management?tab=tool" class="tool-button plain">外部工具更新</router-link>
      </nav>
    </header>
    <SettingsDisplay />
    <SettingsLibrary />
    <SettingsDownloads />
    <SettingsNetwork />
    <SettingsLogins />
  </div>
</template>

<style scoped>
.plain {
  display: inline-flex;
  align-items: center;
  text-decoration: none;
  color: inherit;
}
</style>
