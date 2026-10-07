<script setup lang="ts">
import { computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { NConfigProvider, NMessageProvider, NDialogProvider, zhCN, dateZhCN } from 'naive-ui';
import TraceIcon from './components/TraceIcon.vue';
import ReferenceArt from './components/ReferenceArt.vue';
import { platforms } from './lib/preview';
import { useWorkspaceStore } from './stores/workspace';
import './styles/shell.css';

const route = useRoute();
const router = useRouter();
const workspace = useWorkspaceStore();
const visiblePlatforms = computed(() => workspace.preview ? platforms : platforms.filter(p => p.id === 'bilibili'));
const nav = [
  { path:'/',label:'主页',icon:'home' },
  { path:'/creators',label:'订阅管理',icon:'users' },
  { path:'/timeline',label:'内容库',icon:'file' },
  { path:'/tasks',label:'下载任务',icon:'download' },
];
const theme = { common: { primaryColor:'#3979ff', primaryColorHover:'#6496ff', borderRadius:'8px', fontFamily:'"Segoe UI", "Microsoft YaHei UI", sans-serif' } };
function selectPlatform(id: string) {
  workspace.platform = workspace.platform === id ? '' : id;
  void router.push('/');
}
function search() { void router.push('/'); }
</script>
<template>
  <n-config-provider :locale="zhCN" :date-locale="dateZhCN" :theme-overrides="theme">
    <n-message-provider><n-dialog-provider>
      <div class="trace-app">
        <aside class="sidebar" aria-label="主导航">
          <router-link to="/" class="brand" aria-label="TRACE 主页">
            <svg viewBox="0 0 42 42" class="brand-mark" aria-hidden="true"><path d="M5 4h31c4 0 5 4 2 7L23 25l-4 13c-1 4-6 4-7-1l-1-15L2 10C0 7 2 4 5 4Z" fill="#3c7dff"/><path d="m5 5 19 12-10 21-2-17Z" fill="#8eb8ff"/><path d="m7 7 17 10-9 1" fill="none" stroke="white" stroke-width="2"/></svg>
            <span><strong>TRACE</strong><small>Record Every Trace</small></span>
          </router-link>
          <nav class="main-nav">
            <router-link v-for="item in nav" :key="item.path" :to="item.path" :aria-label="item.label" :class="{active:route.path === item.path}"><TraceIcon :name="item.icon"/><span>{{ item.label }}</span></router-link>
            <button v-if="workspace.preview" disabled title="直播录制将在后续迭代开放"><TraceIcon name="video"/><span>直播录制</span><small>待开放</small></button>
            <button v-if="workspace.preview" disabled title="探索将在后续迭代开放"><TraceIcon name="search"/><span>探索</span><small>待开放</small></button>
          </nav>
          <div class="platform-section">
            <p class="nav-heading">平台</p>
            <button v-for="p in visiblePlatforms" :key="p.id" :class="['platform-nav',{active:workspace.platform === p.id && route.path === '/'}]" @click="selectPlatform(p.id)" :aria-pressed="workspace.platform === p.id"><span :class="['platform-mark',p.id]">{{ p.mark }}</span><span>{{ p.label }}</span><small v-if="workspace.preview">{{ p.count }}</small></button>
            <router-link to="/modules" class="add-platform"><TraceIcon name="plus"/><span>平台管理</span></router-link>
          </div>
          <div class="sidebar-bottom">
            <router-link to="/accounts" class="profile-link"><span class="user-avatar"><ReferenceArt name="portrait" alt="本地用户" :local="workspace.preview"/></span><span>本地档案</span><TraceIcon name="down" :size="14"/></router-link>
            <router-link to="/storage" class="icon-button" aria-label="存储与备份"><TraceIcon name="settings"/></router-link>
          </div>
        </aside>
        <div class="workspace">
          <header class="topbar">
            <form class="global-search" @submit.prevent="search"><TraceIcon name="search"/><input v-model="workspace.query" aria-label="搜索内容" placeholder="搜索用户、内容、关键词..." @input="search"/><button v-if="workspace.query" type="button" class="icon-button" aria-label="清除搜索" @click="workspace.query = ''"><TraceIcon name="close" :size="15"/></button></form>
            <span v-if="workspace.preview" class="preview-badge">界面预览 · 示例数据</span>
            <div class="top-actions"><router-link to="/status" class="icon-button" aria-label="后台状态"><TraceIcon name="bell"/></router-link><router-link to="/tasks" class="icon-button" aria-label="查看下载任务"><TraceIcon name="download"/></router-link><router-link to="/storage" class="icon-button" aria-label="存储设置"><TraceIcon name="settings"/></router-link></div>
          </header>
          <main :class="['page-content',{'utility-page':route.path !== '/'}]"><router-view/></main>
        </div>
      </div>
    </n-dialog-provider></n-message-provider>
  </n-config-provider>
</template>
