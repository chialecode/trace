<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { NConfigProvider, NMessageProvider, NDialogProvider, zhCN, dateZhCN } from "naive-ui";
import { isTauri } from "@tauri-apps/api/core";
import { api } from "./lib/api";
import TraceIcon from "./components/TraceIcon.vue";
import ReferenceArt from "./components/ReferenceArt.vue";
import WindowTitlebar from "./components/WindowTitlebar.vue";
import { platforms } from "./lib/preview";
import { SITES, siteLabel, siteMark } from "./lib/sites";
import { scaledPx, useDisplayStore } from "./stores/display";
import { useWorkspaceStore } from "./stores/workspace";
import "./styles/shell.css";

const route = useRoute();
const router = useRouter();
const workspace = useWorkspaceStore();
const display = useDisplayStore();
display.init();
const nativeWindow = isTauri();
const installedPlatforms = ref(["bilibili"]);
onMounted(async () => {
  if (!workspace.preview)
    try {
      const result = await api.pluginList();
      installedPlatforms.value = result.items.map((p) => p.platform ?? p.plugin_id);
    } catch {
      /* Keep last known navigation while disconnected. */
    }
});
const sitePosition = (id: string) => {
  const at = SITES.findIndex((s) => s.id === id);
  return at < 0 ? SITES.length : at;
};
const visiblePlatforms = computed(() =>
  workspace.preview
    ? platforms
    : [...new Set(installedPlatforms.value)]
        .sort((a, b) => sitePosition(a) - sitePosition(b))
        .map(
          (id) =>
            platforms.find((p) => p.id === id) ?? {
              id,
              label: siteLabel(id),
              mark: siteMark(id),
              count: 0,
            },
        ),
);
const nav = [
  { path: "/", label: "主页", icon: "home" },
  { path: "/acquire", label: "链接归档", icon: "download" },
  { path: "/creators", label: "订阅管理", icon: "users" },
  { path: "/profile", label: "主页勾选", icon: "download" },
  { path: "/timeline", label: "内容库", icon: "file" },
  { path: "/explorer", label: "资源管理器", icon: "collection" },
  { path: "/resources", label: "资源库", icon: "image" },
  { path: "/upgrades", label: "资源升级", icon: "download" },
  { path: "/tasks", label: "下载任务", icon: "download" },
  { path: "/sites", label: "支持的网站", icon: "external" },
  { path: "/settings", label: "设置", icon: "settings" },
];
const theme = computed(() => ({
  common: {
    primaryColor: "#3979ff",
    primaryColorHover: "#6496ff",
    borderRadius: "8px",
    fontFamily: '"Segoe UI", "Microsoft YaHei UI", sans-serif',
    fontSize: scaledPx(14, display.scale),
    fontSizeMini: scaledPx(12, display.scale),
    fontSizeTiny: scaledPx(12, display.scale),
    fontSizeSmall: scaledPx(13, display.scale),
    fontSizeMedium: scaledPx(14, display.scale),
    fontSizeLarge: scaledPx(15, display.scale),
    fontSizeHuge: scaledPx(16, display.scale),
    lineHeight: "1.5715",
  },
  Button: {
    fontSizeTiny: scaledPx(12, display.scale),
    fontSizeSmall: scaledPx(13, display.scale),
    fontSizeMedium: scaledPx(14, display.scale),
    fontSizeLarge: scaledPx(15, display.scale),
    heightSmall: scaledPx(28, display.scale),
    heightMedium: scaledPx(34, display.scale),
    heightLarge: scaledPx(40, display.scale),
  },
  Card: {
    fontSizeSmall: scaledPx(13, display.scale),
    fontSizeMedium: scaledPx(14, display.scale),
    fontSizeLarge: scaledPx(15, display.scale),
    fontSizeHuge: scaledPx(16, display.scale),
    titleFontSizeSmall: scaledPx(15, display.scale),
    titleFontSizeMedium: scaledPx(17, display.scale),
    titleFontSizeLarge: scaledPx(19, display.scale),
    titleFontSizeHuge: scaledPx(21, display.scale),
  },
  DataTable: {
    fontSizeSmall: scaledPx(13, display.scale),
    fontSizeMedium: scaledPx(14, display.scale),
    fontSizeLarge: scaledPx(15, display.scale),
  },
  Dialog: {
    fontSize: scaledPx(14, display.scale),
    titleFontSize: scaledPx(18, display.scale),
  },
  Dropdown: {
    fontSizeSmall: scaledPx(13, display.scale),
    fontSizeMedium: scaledPx(14, display.scale),
    fontSizeLarge: scaledPx(15, display.scale),
    fontSizeHuge: scaledPx(16, display.scale),
    optionHeightSmall: scaledPx(30, display.scale),
    optionHeightMedium: scaledPx(34, display.scale),
    optionHeightLarge: scaledPx(40, display.scale),
    optionHeightHuge: scaledPx(46, display.scale),
  },
  Input: {
    fontSizeSmall: scaledPx(13, display.scale),
    fontSizeMedium: scaledPx(14, display.scale),
    fontSizeLarge: scaledPx(15, display.scale),
    heightSmall: scaledPx(28, display.scale),
    heightMedium: scaledPx(34, display.scale),
    heightLarge: scaledPx(40, display.scale),
  },
  InternalSelectMenu: {
    optionFontSizeSmall: scaledPx(13, display.scale),
    optionFontSizeMedium: scaledPx(14, display.scale),
    optionFontSizeLarge: scaledPx(15, display.scale),
    optionFontSizeHuge: scaledPx(16, display.scale),
    optionHeightSmall: scaledPx(30, display.scale),
    optionHeightMedium: scaledPx(34, display.scale),
    optionHeightLarge: scaledPx(40, display.scale),
    optionHeightHuge: scaledPx(46, display.scale),
  },
  InternalSelection: {
    fontSizeSmall: scaledPx(13, display.scale),
    fontSizeMedium: scaledPx(14, display.scale),
    fontSizeLarge: scaledPx(15, display.scale),
    heightSmall: scaledPx(28, display.scale),
    heightMedium: scaledPx(34, display.scale),
    heightLarge: scaledPx(40, display.scale),
  },
  Popover: {
    fontSize: scaledPx(14, display.scale),
    padding: scaledPx(12, display.scale),
  },
  Tabs: {
    tabFontSizeSmall: scaledPx(13, display.scale),
    tabFontSizeMedium: scaledPx(14, display.scale),
    tabFontSizeLarge: scaledPx(15, display.scale),
    tabFontSizeCard: scaledPx(15, display.scale),
  },
  Tag: {
    fontSizeTiny: scaledPx(11, display.scale),
    fontSizeSmall: scaledPx(13, display.scale),
    fontSizeMedium: scaledPx(14, display.scale),
    fontSizeLarge: scaledPx(15, display.scale),
    heightTiny: scaledPx(20, display.scale),
    heightSmall: scaledPx(24, display.scale),
    heightMedium: scaledPx(28, display.scale),
    heightLarge: scaledPx(32, display.scale),
  },
  Tooltip: {
    padding: scaledPx(8, display.scale),
  },
}));
function selectPlatform(id: string) {
  workspace.platform = workspace.platform === id ? "" : id;
  void router.push("/");
}
function search() {
  void router.push("/");
}
</script>
<template>
  <n-config-provider :locale="zhCN" :date-locale="dateZhCN" :theme-overrides="theme">
    <n-message-provider
      ><n-dialog-provider>
        <div :class="['trace-app', { 'native-window': nativeWindow }]">
          <WindowTitlebar v-if="nativeWindow" />
          <div class="app-body">
            <aside class="sidebar" aria-label="主导航">
              <router-link to="/" class="brand" aria-label="TRACE 主页">
                <svg viewBox="0 0 42 42" class="brand-mark" aria-hidden="true">
                  <path
                    d="M5 4h31c4 0 5 4 2 7L23 25l-4 13c-1 4-6 4-7-1l-1-15L2 10C0 7 2 4 5 4Z"
                    fill="#3c7dff"
                  />
                  <path d="m5 5 19 12-10 21-2-17Z" fill="#8eb8ff" />
                  <path d="m7 7 17 10-9 1" fill="none" stroke="white" stroke-width="2" />
                </svg>
                <span><strong>TRACE</strong><small>Record Every Trace</small></span>
              </router-link>
              <nav class="main-nav">
                <router-link
                  v-for="item in nav"
                  :key="item.path"
                  :to="item.path"
                  :aria-label="item.label"
                  :class="{ active: route.path === item.path }"
                  ><TraceIcon :name="item.icon" /><span>{{ item.label }}</span></router-link
                >
                <button v-if="workspace.preview" disabled title="直播录制将在后续迭代开放">
                  <TraceIcon name="video" /><span>直播录制</span><small>待开放</small>
                </button>
                <button v-if="workspace.preview" disabled title="探索将在后续迭代开放">
                  <TraceIcon name="search" /><span>探索</span><small>待开放</small>
                </button>
              </nav>
              <div class="platform-section">
                <p class="nav-heading">平台</p>
                <button
                  v-for="p in visiblePlatforms"
                  :key="p.id"
                  :class="[
                    'platform-nav',
                    { active: workspace.platform === p.id && route.path === '/' },
                  ]"
                  @click="selectPlatform(p.id)"
                  :aria-pressed="workspace.platform === p.id"
                >
                  <span :class="['platform-mark', p.id]">{{ p.mark }}</span
                  ><span>{{ p.label }}</span
                  ><small v-if="workspace.preview">{{ p.count }}</small>
                </button>
                <router-link to="/modules" class="add-platform"
                  ><TraceIcon name="plus" /><span>平台管理</span></router-link
                >
              </div>
              <div class="sidebar-bottom">
                <router-link to="/accounts" class="profile-link"
                  ><span class="user-avatar"
                    ><ReferenceArt
                      name="portrait"
                      alt="本地用户"
                      :local="workspace.preview" /></span
                  ><span>本地档案</span><TraceIcon name="down" :size="14"
                /></router-link>
                <router-link to="/storage" class="icon-button" aria-label="存储与备份"
                  ><TraceIcon name="settings"
                /></router-link>
              </div>
            </aside>
            <div class="workspace">
              <header class="topbar">
                <form class="global-search" @submit.prevent="search">
                  <TraceIcon name="search" /><input
                    v-model="workspace.query"
                    aria-label="搜索内容"
                    placeholder="搜索用户、内容、关键词..."
                    @input="search"
                  /><button
                    v-if="workspace.query"
                    type="button"
                    class="icon-button"
                    aria-label="清除搜索"
                    @click="workspace.query = ''"
                  >
                    <TraceIcon name="close" :size="15" />
                  </button>
                </form>
                <span v-if="workspace.preview" class="preview-badge">界面预览 · 示例数据</span>
                <router-link v-else to="/status" class="preview-badge"
                  >关闭窗口后继续后台 · 退出管理</router-link
                >
                <div class="top-actions">
                  <router-link to="/status" class="icon-button" aria-label="后台状态"
                    ><TraceIcon name="bell" /></router-link
                  ><router-link to="/tasks" class="icon-button" aria-label="查看下载任务"
                    ><TraceIcon name="download" /></router-link
                  ><router-link to="/storage" class="icon-button" aria-label="存储设置"
                    ><TraceIcon name="settings"
                  /></router-link>
                </div>
              </header>
              <main :class="['page-content', { 'utility-page': route.path !== '/' }]">
                <router-view />
              </main>
            </div>
          </div>
        </div> </n-dialog-provider
    ></n-message-provider>
  </n-config-provider>
</template>
