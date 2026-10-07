<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import {useRoute,useRouter} from 'vue-router';
import { isTauri } from '@tauri-apps/api/core';
import { NButton, NEmpty, NSpin, useMessage } from 'naive-ui';
import TraceIcon from '../components/TraceIcon.vue';
import ReferenceArt from '../components/ReferenceArt.vue';
import { useWorkspaceStore } from '../stores/workspace';
import { filterFeed, platforms, previewEntries, type ContentKind, type FeedEntry } from '../lib/preview';
import { api, mediaUrl, type CreatorDto, type AccountDto, type SubscriptionDto, type MediaDto, type TimelineItemDto } from '../lib/api';
import MediaBrowser from '../components/MediaBrowser.vue';
import ArchiveDocument from '../components/ArchiveDocument.vue';
import LiveRecording from '../components/LiveRecording.vue';
import ArchiveRemoval from '../components/ArchiveRemoval.vue';
import QualityPanel from '../components/QualityPanel.vue';
import ArchiveHistory from '../components/ArchiveHistory.vue';
import AcquisitionHistory from '../components/AcquisitionHistory.vue';
import {authLabel} from '../lib/acquisition';
import type {AuthenticationState} from '../lib/api';
import { readRevision, mergeMedia } from '../lib/archive';
import type { RevisionDto } from '../lib/api';
import type { LiveDto, FragmentDto } from '../lib/api';
import type { DocumentDto } from '../lib/api';
import { formatTime, platformOf, timePrecisionLabel, qualityLabel, saveProgressLabel } from '../lib/format';
import '../styles/home.css';

const workspace = useWorkspaceStore();
function localDayEnd(date:string){const day=new Date(date+'T00:00:00');day.setDate(day.getDate()+1);return day.toISOString();}
const message = useMessage();
const route=useRoute(),router=useRouter();
const focusedEntry=computed(()=>typeof route.query.entry==='string'?route.query.entry:undefined);
const kind = ref('');
const authentication=ref<AuthenticationState|''>('');
const oldest = ref(false);
const fromDate=ref(''),beforeDate=ref(''),minHeight=ref(''),codec=ref(''),saveState=ref('');
const selectedId = ref<string | null>(workspace.preview ? 'preview-1' : null);
const relatedTab = ref(false);
const subscribed = ref(true);
const loading = ref(false);
const error = ref('');
const detailError = ref('');
const detailBody = ref('');
const detailDocument = ref<DocumentDto>();
const detailLive=ref<LiveDto>();
const detailFragments=ref<FragmentDto[]>([]);
const detailMetadata=ref<Record<string,unknown>>({});
const detailObservations=ref<unknown[]>([]);
const detailMediaCursor=ref<string|null>(null);
const detailRevisionId=ref<string>();
const mediaLoading=ref(false);
let mediaPages=1;
const liveEntries = ref<(FeedEntry & { publishedAt: string | null })[]>([]);
const selectionNotice = ref('');
let loadedPages = 0;
let requestedPages = 1;
let appliedScope = '';
const hasMore = ref(false);
const nextCursor = ref<string | null>(null);
const counts = ref<{global:number|null;filtered:number|null}>({global:null,filtered:null});
const creators = ref<CreatorDto[]>([]);
const accounts = ref<AccountDto[]>([]);
const subscriptions = ref<SubscriptionDto[]>([]);
const creatorId = ref('');
const accountId = ref('');
const managementError = ref('');
const savingSubscription = ref(false);
const detailMedia = ref<MediaDto[]>([]);
const visibleAccounts = computed(() => accounts.value.filter(a => !creatorId.value || a.creator_id === creatorId.value));
const creator = computed(() => creators.value.find(c => c.creator_id === creatorId.value));
const subscription = computed(() => subscriptions.value.find(s => s.account_id === accountId.value));
let alive = true;
let requestId = 0;
let detailRequestId = 0;
let searchTimer: ReturnType<typeof setTimeout> | undefined;
let changeTimer: ReturnType<typeof setTimeout> | undefined;
const unlisten: Array<() => void> = [];
async function connectEvents() {
  if (workspace.preview || !isTauri()) return;
  const { listen } = await import('@tauri-apps/api/event');
  const refresh = () => {
    clearTimeout(changeTimer);
    changeTimer = setTimeout(() => { if (alive) { void load(false, true); void loadManagement(); } }, 250);
  };
  for (const [event, handler] of [
    ['trace://change', refresh], ['trace://resync', refresh],
    ['trace://disconnected', () => { if (alive) error.value = '后台连接已断开，重新连接后会刷新档案。'; }],
  ] as const) {
    const stop = await listen(event, handler);
    if (alive) unlisten.push(stop); else stop();
  }
}
const allTabs = [
  {label:'全部',value:'',icon:'bell'}, {label:'动态',value:'activity',icon:'activity'},
  {label:'视频',value:'video',icon:'play'}, {label:'直播',value:'live',icon:'video'},
  {label:'图片',value:'image',icon:'image'}, {label:'文章',value:'article',icon:'file'},
  {label:'合集',value:'collection',icon:'collection'},
];
const tabs = computed(() => workspace.preview ? allTabs : allTabs.filter(t => t.value !== 'collection').map(t => t.value === 'activity' ? {...t,value:'dynamic'} : t));
const entries = computed(() => workspace.preview ? previewEntries : liveEntries.value);
const filtered = computed(() => workspace.preview ? filterFeed(entries.value, workspace.query, kind.value, workspace.platform, oldest.value) : liveEntries.value);
const groups = computed(() => Array.from(new Set(filtered.value.map(x => x.day))).map(day => ({ day, items:filtered.value.filter(x => x.day === day) })));
const selected = computed(() => filtered.value.find(x => x.id === selectedId.value) ?? null);
const related = computed(() => entries.value.filter(x => x.id !== selectedId.value).slice(0, 3));
const stats = computed(() => workspace.preview
  ? [{label:'全部内容',value:'1,284',icon:'file',kind:''},{label:'视频',value:'428',icon:'play',kind:'video'},{label:'图文',value:'536',icon:'image',kind:'image'},{label:'直播',value:'142',icon:'video',kind:'live'},{label:'合集',value:'178',icon:'collection',kind:'collection'}]
  : [{label:'全档案内容',value:String(counts.value.global ?? '—'),icon:'file',kind:''},{label:'符合筛选',value:String(counts.value.filtered ?? '—'),icon:'play',kind:kind.value}]);
function mark(platform: string) { return platforms.find(x => x.id === platform)?.mark ?? '·'; }
function resetFilters() { if(focusedEntry.value)void router.replace({path:'/',query:{}}); workspace.query = ''; workspace.platform = ''; kind.value = '';fromDate.value='';beforeDate.value='';minHeight.value='';codec.value='';saveState.value='';authentication.value=''; }
function previewAction() { message.info('这是界面预览，示例内容不会创建下载任务或写入档案。'); }
function toggleSubscription() { subscribed.value = !subscribed.value; message.info(subscribed.value ? '预览：已订阅（未写入档案）' : '预览：已暂停订阅（未写入档案）'); }
async function openSource() {
  if (workspace.preview) { previewAction(); return; }
  if (!selected.value?.sourceUrl) return;
  try { await api.openSource(selected.value.id); } catch { message.error('无法打开来源，请检查后台连接。'); }
}
function selectEntry(entry: FeedEntry) { selectedId.value = entry.id; relatedTab.value = false; selectionNotice.value = ''; }
function mapEntry(item: TimelineItemDto): FeedEntry & { publishedAt: string | null } {
  return {
    id:item.entry_id,platform:item.namespace.includes('bilibili') ? 'bilibili' : item.namespace,
    platformLabel:platformOf(item.namespace),kind:(item.kind === 'dynamic' ? 'activity' : item.kind === 'opus' ? 'image' : item.kind) as ContentKind,title:item.title,
    body:'',time:formatTime(item.published_at),day:item.published_at?.slice(0,10) ?? '发布时间未知',
    tags:[],art:'unavailable',saveLabel:saveProgressLabel(item.save_progress),qualityLabel:qualityLabel(item.quality_state),
    coverUrl:mediaUrl(item.cover_blob_id),sourceUrl:item.source_url,sourceKey:item.source_key,
    creatorName:item.creator_name,accountName:item.account_name ?? item.native_account_key,precision:item.published_precision,
    publishedAt:item.published_at,authenticationLabel:authLabel(item.authentication?.state),
  };
}
async function load(append = false, refresh = false) {
  if (workspace.preview) return;
  const current = ++requestId;
  const options = {authentication:authentication.value||undefined,entryId:focusedEntry.value,limit:50, creatorId:creatorId.value || undefined, accountId:accountId.value || undefined, query:workspace.query.trim() || undefined, namespace:workspace.platform || undefined, kind:kind.value || undefined, oldest:oldest.value,publishedFrom:fromDate.value?new Date(fromDate.value+'T00:00:00').toISOString():undefined,publishedBefore:beforeDate.value?localDayEnd(beforeDate.value):undefined,minHeight:minHeight.value?Number(minHeight.value):undefined,codec:codec.value||undefined,saveState:saveState.value||undefined};
  const scope = JSON.stringify(options);
  const sameScope = appliedScope === scope;
  append = append && sameScope;
  const boundary = refresh && sameScope ? liveEntries.value.at(-1) : undefined;
  const targetPages = append ? loadedPages + 1 : refresh && sameScope ? requestedPages : 1;
  requestedPages = targetPages;
  loading.value = true; error.value = '';
  try {
    let cursor = append ? nextCursor.value : null;
    let pages = append ? loadedPages : 0;
    let mapped = append ? [...liveEntries.value] : [];
    let latestCounts: typeof counts.value = {global:null,filtered:null};
    do {
      const result = await api.archiveTimeline({...options,cursor:cursor ?? undefined});
      if (current !== requestId || !alive) return;
      mapped.push(...result.items.map(mapEntry));
      pages++;
      if (cursor && result.next_cursor === cursor) throw new Error('non advancing cursor');
      cursor = result.next_cursor;
      latestCounts = result.counts ?? {global:null,filtered:null};
      const tail = mapped.at(-1);
      // Follow the previous window's sort boundary even when new entries push it to another page.
      // A removed boundary still has a comparable time/id, so it does not force an archive-wide scan.
      const reachedBoundary = !boundary || !tail || (() => {
        const rank = Number(tail.publishedAt === null) - Number(boundary.publishedAt === null);
        if (rank) return rank > 0;
        const a = tail.publishedAt ?? '', b = boundary.publishedAt ?? '';
        const order = a < b ? -1 : a > b ? 1 : tail.id < boundary.id ? -1 : tail.id > boundary.id ? 1 : 0;
        return options.oldest ? order >= 0 : order <= 0;
      })();
      if (append || (pages >= targetPages && reachedBoundary)) break;
    } while (cursor);
    if (current !== requestId || !alive) return;
    mapped = [...new Map(mapped.map(entry => [entry.id,entry])).values()];
    const previous = selectedId.value;
    if (refresh && sameScope && previous && !mapped.some(entry => entry.id === previous)) {
      const membership = await api.archiveTimeline({...options,entryId:previous,limit:1});
      if (current !== requestId || !alive) return;
      if (selectedId.value !== previous) { void load(false, true); return; }
      const stillMatches = membership.items.find(entry => entry.entry_id === previous);
      if (stillMatches) mapped.push(mapEntry(stillMatches));
    }
    const retained = previous !== null && mapped.some(entry => entry.id === previous);
    // Publish a coherent replacement without transiently emptying the selected detail/player.
    liveEntries.value = mapped;
    nextCursor.value = cursor; hasMore.value = Boolean(cursor); counts.value = latestCounts;
    loadedPages = pages; requestedPages = pages; appliedScope = scope;
    if (previous && !retained) selectionNotice.value = '原选择已不在当前结果中，已切换到可用内容。';
    if (refresh && retained) void loadDetail(previous, true);
  } catch {
    if (current === requestId) error.value = '暂时无法读取档案。请检查后台连接；搜索索引清理或预算不足时，请在存储页重建或调整预算。';
  } finally { if (current === requestId) loading.value = false; }
}
async function loadManagement() {
  if (workspace.preview) return;
  try {
    const [c,a,s] = await Promise.all([api.creatorList(),api.accountList(),api.subscriptionList()]);
    if (!alive) return;
    creators.value=c.items; accounts.value=a.items; subscriptions.value=s.items;
    managementError.value='';
  } catch { if (alive) managementError.value='创作者或订阅读取失败，请重试。'; }
}
async function updateSubscription() {
  if (workspace.preview || !accountId.value) return;
  savingSubscription.value=true;
  try {
    if (subscription.value) await api.subscriptionSetEnabled(subscription.value.subscription_id,subscription.value.state!=='enabled');
    else await api.subscriptionCreate(accountId.value,'from_now');
    await loadManagement();
    if (alive) message.success('订阅设置已保存。');
  } catch { if (alive) message.error('订阅未能保存，请检查后台后重试。'); }
  finally { savingSubscription.value=false; }
}
watch(filtered, list => { if (!list.some(x => x.id === selectedId.value)) selectedId.value = list[0]?.id ?? null; });
async function loadDetail(id: string | null, preserve = false) {
  const current = ++detailRequestId;
  detailError.value = '';
  if (!preserve) { detailBody.value = ''; detailMedia.value = []; detailDocument.value = undefined; detailLive.value=undefined;detailFragments.value=[]; detailMetadata.value={};detailObservations.value=[];detailMediaCursor.value=null;detailRevisionId.value=undefined;mediaPages=1;mediaLoading.value=false; }
  if (!id || workspace.preview) return;
  try {
    const result = await api.archiveEntryGet(id);
    const isCurrent=()=>alive && current===detailRequestId && selectedId.value===id;
    const revisions=result.revisions as RevisionDto[];
    const revisionId=(result.entry as {current_revision_id?:string}).current_revision_id;
    let revision=revisions.find(r=>r.revision_id===revisionId) ?? [...revisions].reverse().find(r=>r.completeness==='full') ?? revisions.at(-1);
    if(revision) revision=await readRevision(id,revision,isCurrent);
    let media=result.media??[];
    let cursor=result.media_cursor??null;
    const pages=preserve?mediaPages:1;
    for(let page=1;page<pages && cursor;page++){
      if(!isCurrent())return;
      const next=await api.archiveEntryPage<MediaDto>(id,'media',cursor,revisionId);
      media=mergeMedia(media,next.items);cursor=next.next_cursor;
    }
    if (alive && current === detailRequestId && selectedId.value === id) {
      detailBody.value = revision?.body ?? String((result.entry as Record<string, unknown>).body ?? '');
      detailMetadata.value=revision?.metadata??{};
      detailObservations.value=result.observations;
      detailDocument.value = revision?.document;
      detailMedia.value = media;detailMediaCursor.value=cursor;detailRevisionId.value=revisionId;
      detailLive.value=result.live ?? undefined;detailFragments.value=result.fragments ?? [];
    }
  } catch { if (alive && current === detailRequestId) detailError.value = '详情读取失败，请重新选择条目重试。'; }
}
async function moreMedia(){
  const id=selectedId.value,cursor=detailMediaCursor.value,current=detailRequestId;
  if(!id || !cursor || mediaLoading.value)return;
  mediaLoading.value=true;
  try{const page=await api.archiveEntryPage<MediaDto>(id,'media',cursor,detailRevisionId.value);if(alive && current===detailRequestId){detailMedia.value=mergeMedia(detailMedia.value,page.items);detailMediaCursor.value=page.next_cursor;mediaPages++;}}
  catch{if(alive && current===detailRequestId)detailError.value='更多资源读取失败，请重试。';}
  finally{if(alive && current===detailRequestId)mediaLoading.value=false;}
}
watch(focusedEntry,id=>{if(id){workspace.query='';workspace.platform='';kind.value='';creatorId.value='';accountId.value='';fromDate.value='';beforeDate.value='';minHeight.value='';codec.value='';saveState.value='';}},{immediate:true});
watch(selectedId, id => { void loadDetail(id); });
watch([focusedEntry,() => workspace.query, () => workspace.platform, kind, oldest, creatorId, accountId,fromDate,beforeDate,minHeight,codec,saveState,authentication], () => {
  if (workspace.preview) return;
  clearTimeout(searchTimer);
  ++requestId; // A pending response must not overwrite a new search.
  requestedPages = 1;
  searchTimer = setTimeout(() => { void load(); }, 250);
});
watch(creatorId, () => { accountId.value = ''; });
onMounted(() => { void load(false, true); void loadManagement(); void connectEvents().catch(() => { if (alive) error.value = '后台事件连接失败，请重试或重新打开窗口。'; }); });
onBeforeUnmount(() => { alive = false; clearTimeout(searchTimer); clearTimeout(changeTimer); unlisten.forEach(stop => stop()); ++requestId; ++detailRequestId; });
async function recheckEntry(){
  if(!selectedId.value)return;try{await api.archiveEntryRecheck(selectedId.value);selectionNotice.value='已安排此内容的详情与资源补查。';}catch{selectionNotice.value='补查未能启动，请确认来源订阅已启用并查看后台状态。';}
}
</script>

<template>
  <div class="home-layout">
    <section class="feed-column" aria-label="创作者时间线">
      <section class="creator-banner">
        <div class="banner-art"><ReferenceArt name="banner" alt="" :local="workspace.preview"/></div>
        <div class="creator-avatar"><ReferenceArt :name="workspace.preview ? 'portrait' : 'unavailable'" :alt="workspace.preview ? '森中人头像（示例）' : '档案头像'" :local="workspace.preview"/></div>
        <div class="creator-info">
          <h1>{{ workspace.preview ? '森中人' : (creator?.display_name ?? '我的档案') }}<span v-if="workspace.preview" class="verified" aria-label="示例认证"><TraceIcon name="check" :size="12"/></span></h1>
          <p class="creator-handle">{{ workspace.preview ? '@morinaka_jin' : '记录、收集，留住每一份热爱。' }}</p>
          <p class="creator-bio">{{ workspace.preview ? '分享动画、游戏与生活的随笔。' : (creator?.notes || '从创作者出发，浏览已收录的内容。') }}</p>
          <div v-if="workspace.preview" class="creator-platforms"><span><b class="mini-mark bilibili">bili</b>Bilibili</span><span><b class="mini-mark youtube">▶</b>YouTube</span><span>𝕏 <small>X +2</small></span></div>
          <div v-else class="creator-picker"><label>创作者 <select v-model="creatorId" aria-label="选择创作者"><option value="">全部创作者</option><option v-for="c in creators" :key="c.creator_id" :value="c.creator_id">{{ c.display_name }}</option></select></label><label>来源账号 <select v-model="accountId" aria-label="选择来源账号"><option value="">选择账号</option><option v-for="a in visibleAccounts" :key="a.account_id" :value="a.account_id">{{ platformOf(a.namespace) }} · {{ a.display_name || a.native_account_key }}</option></select></label><p v-if="managementError" role="alert">{{ managementError }} <button @click="loadManagement">重试</button></p></div>
          <router-link v-if="!workspace.preview" to="/creators" class="creator-manage">管理创作者与订阅 <TraceIcon name="chevron" :size="13"/></router-link>
        </div>
        <div class="subscribe-actions">
          <button v-if="workspace.preview" class="primary-button" @click="toggleSubscription"><TraceIcon :name="subscribed ? 'bell' : 'plus'" :size="17"/>{{ subscribed ? '订阅中' : '订阅' }}</button>
          <button v-else-if="accountId" class="primary-button" :disabled="savingSubscription" @click="updateSubscription">{{ savingSubscription ? '正在保存…' : subscription?.state === 'enabled' ? '暂停订阅' : subscription ? '恢复订阅' : '订阅此账号' }}</button><router-link v-else class="primary-button" to="/creators"><TraceIcon name="plus" :size="16"/>添加订阅</router-link>
          <button v-if="workspace.preview" class="banner-more icon-button" aria-label="预览说明" @click="previewAction"><TraceIcon name="more"/></button>
        </div>
      </section>

      <div class="content-tabs" aria-label="内容类型">
        <button v-for="tab in tabs" :key="tab.value" :class="{active:kind === tab.value}" :aria-pressed="kind === tab.value" @click="kind = tab.value"><TraceIcon :name="tab.icon" :size="15"/>{{ tab.label }}</button>
        <label class="sort-control"><span class="sr-only">时间排序</span><select v-model="oldest" aria-label="时间排序"><option :value="false">按时间排序</option><option :value="true">最早发布优先</option></select></label>
      </div>
      <label v-if="!workspace.preview" class="authentication-filter">获取身份 <select v-model="authentication" aria-label="获取身份"><option value="">全部获取历史</option><option value="authenticated">登录时间线</option><option value="anonymous">未登录时间线</option><option value="expired">认证已失效</option><option value="unknown">身份未知</option></select></label>
      <details v-if="!workspace.preview" class="archive-filters"><summary>日期、规格与保存状态</summary><div>
        <label>起始日期 <input v-model="fromDate" type="date" aria-label="起始日期"/></label>
        <label>结束日期 <input v-model="beforeDate" type="date" aria-label="结束日期"/></label>
        <label>最低高度 <select v-model="minHeight" aria-label="最低高度"><option value="">不限</option><option value="720">720p</option><option value="1080">1080p</option><option value="2160">2160p</option></select></label>
        <label>视频编码 <select v-model="codec" aria-label="视频编码"><option value="">不限</option><option value="h264">H.264</option><option value="hevc">HEVC</option><option value="av1">AV1</option></select></label>
        <label>保存状态 <select v-model="saveState" aria-label="保存状态"><option value="">不限</option><option value="complete">完整</option><option value="partial">部分</option><option value="pending">待保存</option><option value="unknown">未知</option></select></label>
        <button @click="resetFilters">清除筛选</button><small>按当前正文版本已验证媒体的实际规格筛选；日期使用本地时区，未知发布时间不匹配日期范围。</small>
      </div></details>
      <div class="stats-bar" :aria-label="workspace.preview ? '示例统计' : '全档案与筛选统计'">
        <button v-for="stat in stats" :key="stat.label" @click="kind = stat.kind"><TraceIcon :name="stat.icon" :size="17"/><span>{{ stat.label }}<strong>{{ stat.value }}</strong></span></button>
      </div>
      <div v-if="workspace.query || workspace.platform" class="filter-summary"><span>{{ workspace.preview ? '示例筛选' : '当前已加载' }} · {{ filtered.length }} 条内容</span><button @click="resetFilters">清除筛选</button></div>

      <n-spin :show="loading">
        <p v-if="focusedEntry" class="selection-notice">当前定位到指定发布记录。<button @click="resetFilters">返回完整时间线</button></p>
        <div v-if="error" class="feed-state" role="alert"><h3>无法连接档案</h3><p>{{ error }}</p><n-button @click="load(false)">重试</n-button><router-link to="/status">查看后台状态</router-link></div>
        <div v-else-if="!loading && !filtered.length" class="feed-state"><n-empty :description="entries.length ? '没有符合条件的内容' : '还没有收录内容'"/><n-button v-if="entries.length" @click="resetFilters">查看全部内容</n-button><router-link v-else to="/creators">添加第一个创作者</router-link></div>
        <div v-else class="timeline">
          <section v-for="(group,index) in groups" :key="group.day" class="day-group">
            <h2 class="day-heading"><span class="day-square"></span><strong>{{ workspace.preview ? (group.day === '2025-09-24' ? '今天' : '昨天') : '发布于' }}</strong><time>{{ group.day }}</time><span v-if="workspace.preview && index === 0" class="sample-date">示例时间线</span></h2>
            <div class="timeline-track">
              <article v-for="entry in group.items" :key="entry.id" :class="['feed-card',{'selected':selectedId === entry.id,'compact':entry.kind !== 'video'}]">
                <span class="timeline-dot"></span>
                <div class="entry-platform"><span :class="['platform-mark',entry.platform]">{{ mark(entry.platform) }}</span><span>{{ entry.platformLabel }}</span><time>{{ entry.time }}</time><button class="icon-button card-more" :aria-label="'查看详情：' + entry.title" @click="selectEntry(entry)"><TraceIcon name="more" :size="17"/></button></div>
                <div class="entry-content">
                  <button class="entry-copy" @click="selectEntry(entry)" :aria-label="'查看：' + entry.title"><h3>{{ entry.title }}</h3><p v-if="entry.body">{{ entry.body }}</p><p v-if="!workspace.preview">{{ entry.creatorName }} · {{ entry.accountName }}</p><p v-if="!workspace.preview">{{ entry.saveLabel }} · {{ entry.qualityLabel }} · {{entry.authenticationLabel}}</p></button>
                  <button class="entry-cover" @click="selectEntry(entry)" :aria-label="'选择内容：' + entry.title"><img v-if="entry.coverUrl && !workspace.preview" :src="entry.coverUrl" :alt="entry.title" loading="lazy" @error="entry.coverUrl = ''"/><ReferenceArt v-else :name="entry.art" :alt="entry.title" :local="workspace.preview"/><span v-if="entry.duration" class="duration">{{ entry.duration }}</span><span v-if="!workspace.preview && !entry.coverUrl" class="cover-placeholder">暂无封面</span></button>
                  <div class="entry-tags"><button v-for="tag in entry.tags" :key="tag" @click="workspace.query = tag"># {{ tag }}</button></div>
                  <div v-if="workspace.preview && entry.views" class="entry-metrics"><span><TraceIcon name="play" :size="15"/>{{ entry.views }}</span><span><TraceIcon name="chat" :size="15"/>{{ entry.comments }}</span><span><TraceIcon name="heart" :size="16"/>{{ entry.likes }}</span><button class="icon-button" :aria-label="'下载示例：' + entry.title" @click="previewAction"><TraceIcon name="download" :size="16"/></button></div>
                </div>
              </article>
            </div>
          </section>
        </div>
      </n-spin>
      <button v-if="hasMore && !workspace.preview" class="more-content" :disabled="loading" @click="load(true)">加载更多内容 <TraceIcon name="chevron" :size="15"/></button>
    </section>

    <aside class="detail-column" aria-label="内容详情">
      <div class="detail-tabs"><button :class="{active:!relatedTab}" :aria-pressed="!relatedTab" @click="relatedTab = false"><span class="detail-dot">◉</span>详情</button><button :class="{active:relatedTab}" :aria-pressed="relatedTab" @click="relatedTab = true">相关内容</button></div>
      <p v-if="selectionNotice" role="status">{{ selectionNotice }}</p>
      <div v-if="selected && !relatedTab" class="detail-body">
        <div class="detail-cover"><img v-if="selected.coverUrl && !workspace.preview" :src="selected.coverUrl" :alt="selected.title" @error="selected.coverUrl = ''"/><ReferenceArt v-else :name="selected.art" :alt="selected.title" :local="workspace.preview"/><span class="source-chip"><span :class="['mini-mark',selected.platform]">{{ mark(selected.platform) }}</span>{{ selected.platformLabel }}</span><span v-if="selected.duration" class="duration">{{ selected.duration }}</span><span v-if="!workspace.preview && !selected.coverUrl" class="cover-placeholder">暂无封面</span></div>
        <h2 class="detail-title">{{ selected.title }}</h2>
        <p class="detail-byline"><span class="tiny-avatar"><ReferenceArt name="portrait" alt="" :local="workspace.preview"/></span>{{ workspace.preview ? '森中人' : (selected.creatorName || selected.accountName || '档案内容') }}<span>·</span>{{ selected.platformLabel }}<span>·</span>{{ selected.time }}</p>
        <p v-if="detailError" class="detail-description" role="alert">{{ detailError }}</p>
        <p v-else-if="workspace.preview" class="detail-description">{{ selected.body }}</p>
        <ArchiveDocument v-else class="detail-description" :document="detailDocument" :media="detailMedia" :body="detailBody"/>
        <details v-if="!workspace.preview && Object.keys(detailMetadata).length"><summary>来源信息与观察记录</summary><pre class="metadata-text">{{ JSON.stringify(detailMetadata,null,2) }}</pre><pre class="metadata-text">{{ JSON.stringify(detailObservations,null,2) }}</pre></details>
        <ArchiveRemoval v-if="!workspace.preview && selectedId" :key="`remove-${selectedId}`" :entry-id="selectedId"/>
        <AcquisitionHistory v-if="!workspace.preview && selectedId" :key="`acquisition-${selectedId}`" :entry-id="selectedId"/>
        <ArchiveHistory v-if="!workspace.preview && selectedId" :key="selectedId" :entry-id="selectedId"/>
        <QualityPanel v-if="!workspace.preview && selectedId && detailMedia.length" :entry-id="selectedId"/>
        <LiveRecording v-if="detailLive" :live="detailLive" :fragments="detailFragments" @refresh="loadDetail(selectedId,true)"/>
        <div class="entry-tags detail-tags"><button v-for="tag in selected.tags" :key="tag" @click="workspace.query = tag"># {{ tag }}</button></div>
        <section class="detail-section"><h3>来源信息</h3><div class="source-row"><TraceIcon name="play" :size="15"/><span>{{ workspace.preview ? (selected.platform === 'bilibili' ? 'https://www.bilibili.com/video/BV1...' : selected.platformLabel + ' · 示例来源') : (selected.sourceUrl || '来源标识：' + (selected.sourceKey || selected.id)) }}</span><TraceIcon name="external" :size="14"/></div></section>
        <section class="detail-section"><h3>{{ workspace.preview ? '已保存到' : '保存状态' }}<small v-if="workspace.preview">示例</small></h3>
          <div v-if="workspace.preview" class="saved-files"><div><span class="file-icon"><TraceIcon :name="selected.kind === 'video' ? 'play' : 'image'" :size="15"/></span>{{ selected.kind === 'video' ? '视频' : '图片' }}<small>{{ selected.kind === 'video' ? '1.2 GB' : '4.8 MB' }}</small></div><div><span class="file-icon"><TraceIcon name="image" :size="15"/></span>封面图片<small>4.8 MB</small></div><div><span class="file-icon"><TraceIcon name="file" :size="15"/></span>文案<small>1.3 KB</small></div></div>
          <div v-else class="saved-files"><p>{{ selected.saveLabel }}</p><p>质量：{{ selected.qualityLabel }}</p><small>发布时间精度：{{ timePrecisionLabel(selected.precision) }}</small><MediaBrowser :key="selectedId ?? undefined" :media="detailMedia"/><button v-if="detailMediaCursor" :disabled="mediaLoading" @click="moreMedia">{{mediaLoading?'正在读取…':'加载更多资源'}}</button></div>
        </section>
        <section class="detail-section"><h3>操作</h3><div class="detail-actions"><button :disabled="!workspace.preview && !selected.sourceUrl" @click="openSource"><TraceIcon name="external" :size="18"/>在浏览器中打开</button><button v-if="workspace.preview" @click="previewAction"><TraceIcon name="download" :size="18"/>下载到本地</button><button v-if="workspace.preview" @click="previewAction"><TraceIcon name="more" :size="18"/>更多操作<TraceIcon name="chevron" :size="14"/></button><button v-else @click="recheckEntry">补查此内容</button><router-link v-if="!workspace.preview" to="/timeline">在内容库查看<TraceIcon name="chevron" :size="14"/></router-link></div></section>
        <section v-if="workspace.preview" class="detail-section related-section"><h3>相关内容</h3><button v-for="entry in related.slice(0,2)" :key="entry.id" class="related-item" @click="resetFilters(); selectEntry(entry)"><span class="related-cover"><img v-if="entry.coverUrl && !workspace.preview" :src="entry.coverUrl" :alt="entry.title" loading="lazy" @error="entry.coverUrl = ''"/><ReferenceArt v-else :name="entry.art" :alt="entry.title" :local="workspace.preview"/></span><span><strong>{{ entry.title }}</strong><small><b :class="['mini-mark',entry.platform]">{{ mark(entry.platform) }}</b>{{ entry.platformLabel }} · {{ entry.time }}</small></span></button></section>
      </div>
      <div v-else-if="relatedTab" class="detail-body"><p class="related-note">{{ workspace.preview ? '同一创作者的其他示例内容' : '当前档案中其他已载入的内容' }}</p><button v-for="entry in related" :key="entry.id" class="related-item" @click="resetFilters(); selectEntry(entry)"><span class="related-cover"><img v-if="entry.coverUrl && !workspace.preview" :src="entry.coverUrl" :alt="entry.title" loading="lazy" @error="entry.coverUrl = ''"/><ReferenceArt v-else :name="entry.art" :alt="entry.title" :local="workspace.preview"/></span><span><strong>{{ entry.title }}</strong><small>{{ entry.platformLabel }} · {{ entry.time }}</small></span></button><n-empty v-if="!related.length" description="暂无相关内容"/></div>
      <div v-else class="detail-empty"><TraceIcon name="file" :size="30"/><p>选择一条内容，查看详情</p></div>
    </aside>
  </div>
</template>
