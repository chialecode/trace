import { mount, flushPromises } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { defineComponent } from 'vue';
import { createRouter, createMemoryHistory } from 'vue-router';
import { NMessageProvider } from 'naive-ui';
import HomeView from '../HomeView.vue';
import App from '../../App.vue';
import { api } from '../../lib/api';
import { isTauri } from '@tauri-apps/api/core';
import { listen } from '@tauri-apps/api/event';
vi.mock('@tauri-apps/api/event', () => ({listen:vi.fn()}));
vi.mock('../../lib/api', async (importOriginal) => { const original = await importOriginal<typeof import('../../lib/api')>(); return {...original, api:{...original.api,archiveTimeline:vi.fn(), searchQuery:vi.fn(),archiveEntryGet:vi.fn(),creatorList:vi.fn(),accountList:vi.fn(),subscriptionList:vi.fn(),subscriptionSetEnabled:vi.fn(),subscriptionCreate:vi.fn(),openExternal:vi.fn()}}; });
import { useWorkspaceStore } from '../../stores/workspace';
import { filterFeed, previewEntries } from '../../lib/preview';
import type { TimelineItemDto, MediaDto } from '../../lib/api';

vi.mock('@tauri-apps/api/core', () => ({ isTauri: vi.fn(() => false), invoke: vi.fn() }));

async function setup(preview = true, shell = false) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const store = useWorkspaceStore();
  store.preview = preview;
  const router = createRouter({ history:createMemoryHistory(), routes:[
    {path:'/',component:HomeView},{path:'/creators',component:{template:'<div>订阅管理页</div>'}},
    {path:'/status',component:{template:'<div>后台状态页</div>'}},
  ]});
  await router.push('/');
  await router.isReady();
  const wrapped = defineComponent({ components:{HomeView,NMessageProvider}, template:'<NMessageProvider><HomeView/></NMessageProvider>' });
  const wrapper = mount(shell ? App : wrapped,{global:{plugins:[pinia,router]}});
  await flushPromises();
  return {wrapper,store,router};
}
beforeEach(() => { vi.resetAllMocks();
  vi.mocked(api.creatorList).mockResolvedValue({items:[],next_cursor:null});
  vi.mocked(api.accountList).mockResolvedValue({items:[],next_cursor:null});
  vi.mocked(api.subscriptionList).mockResolvedValue({items:[],next_cursor:null});
});

const reworkEntry = (id: string): TimelineItemDto => ({entry_id:id,current_revision_id:'r-'+id,title:id,namespace:'bilibili',kind:'video',published_at:'2026-10-07T00:00:00Z',published_precision:'source'});
const reworkMedia: MediaDto[] = ['a','b'].map((key,index) => ({media_id:key,variant_id:key,blob_id:'sha256:'+key.repeat(64),role:'video',position:index,available:true,mime:'video/mp4',size:128}));
function changeEvent(topic='entry.updated') {
  const change=vi.mocked(listen).mock.calls.find(c=>c[0]==='trace://change')?.[1];
  expect(change).toBeDefined();
  change?.({event:'trace://change',id:1,payload:{topic,aggregate_id:'e2'}});
}

describe('F-016 refresh reconciliation', () => {
  it('retains a still-matching selection that moved outside the loaded sort window', async () => {
    vi.mocked(isTauri).mockReturnValue(true);vi.mocked(listen).mockResolvedValue(vi.fn());
    vi.mocked(api.archiveTimeline).mockResolvedValueOnce({items:['e3','e2'].map(reworkEntry),next_cursor:'more'}).mockResolvedValueOnce({items:['e4','e1'].map(reworkEntry),next_cursor:'moved'}).mockResolvedValueOnce({items:[{...reworkEntry('e2'),published_at:'2020-01-01T00:00:00Z'}],next_cursor:null});
    vi.mocked(api.archiveEntryGet).mockResolvedValue({entry:{},revisions:[],observations:[],media:reworkMedia});
    const {wrapper}=await setup(false);
    await wrapper.findAll('.entry-copy')[1]!.trigger('click');await flushPromises();
    const player=wrapper.get('video').element as HTMLVideoElement;player.currentTime=19;
    vi.useFakeTimers();
    try {
      changeEvent();await vi.advanceTimersByTimeAsync(260);await flushPromises();
      expect(api.archiveTimeline).toHaveBeenLastCalledWith(expect.objectContaining({entryId:'e2',limit:1}));
      expect(wrapper.get('.detail-title').text()).toBe('e2');
      expect(wrapper.get('video').element).toBe(player);expect(player.currentTime).toBe(19);
      expect(wrapper.text()).not.toContain('原选择已不在当前结果中');
    } finally {wrapper.unmount();vi.useRealTimers();}
  });
  it('retains loaded pages, selected variant, node and playback position across related updates', async () => {
    vi.mocked(isTauri).mockReturnValue(true); vi.mocked(listen).mockResolvedValue(vi.fn());
    vi.mocked(api.archiveTimeline)
      .mockResolvedValueOnce({items:['e5','e4'].map(reworkEntry),next_cursor:'old-1'})
      .mockResolvedValueOnce({items:['e3','e2'].map(reworkEntry),next_cursor:'old-2'})
      .mockResolvedValueOnce({items:['e6','e5'].map(reworkEntry),next_cursor:'new-1'})
      .mockResolvedValueOnce({items:['e4','e3'].map(reworkEntry),next_cursor:'new-2'})
      .mockResolvedValueOnce({items:['e2','e1'].map(reworkEntry),next_cursor:null});
    vi.mocked(api.archiveEntryGet).mockResolvedValue({entry:{body:'before'},revisions:[],observations:[],media:reworkMedia});
    const {wrapper}=await setup(false);
    await wrapper.get('.more-content').trigger('click');await flushPromises();
    await wrapper.findAll('.entry-copy')[3]!.trigger('click');await flushPromises();
    await wrapper.get('select[aria-label="选择媒体资源"]').setValue('b');
    const player=wrapper.get('video').element as HTMLVideoElement; player.currentTime=37;
    vi.mocked(api.archiveEntryGet).mockResolvedValue({entry:{body:'updated'},revisions:[],observations:[],media:reworkMedia.map(m=>({...m}))});
    vi.useFakeTimers();
    try {
      changeEvent(); await vi.advanceTimersByTimeAsync(260); await flushPromises();
      expect(api.archiveTimeline).toHaveBeenCalledTimes(5);
      expect(wrapper.findAll('.feed-card')).toHaveLength(6);
      expect(wrapper.get('.detail-title').text()).toBe('e2');
      expect(wrapper.get('.detail-description').text()).toBe('updated');
      expect((wrapper.get('select[aria-label="选择媒体资源"]').element as HTMLSelectElement).value).toBe('b');
      expect(wrapper.get('video').element).toBe(player);
      expect(player.currentTime).toBe(37);
    } finally {wrapper.unmount();vi.useRealTimers();}
  });

  it('switches only after the selected entry leaves the refreshed result and explains the change', async () => {
    vi.mocked(isTauri).mockReturnValue(true); vi.mocked(listen).mockResolvedValue(vi.fn());
    vi.mocked(api.archiveTimeline).mockResolvedValueOnce({items:['e3','e2'].map(reworkEntry),next_cursor:null}).mockResolvedValueOnce({items:[reworkEntry('e3')],next_cursor:null}).mockResolvedValueOnce({items:[],next_cursor:null});
    vi.mocked(api.archiveEntryGet).mockResolvedValue({entry:{},revisions:[],observations:[],media:reworkMedia});
    const {wrapper}=await setup(false);
    await wrapper.findAll('.entry-copy')[1]!.trigger('click');await flushPromises();
    const player=wrapper.get('video').element;
    vi.useFakeTimers();
    try {
      changeEvent(); await vi.advanceTimersByTimeAsync(260);await flushPromises();
      expect(wrapper.get('.detail-title').text()).toBe('e3');
      expect(wrapper.text()).toContain('原选择已不在当前结果中');
      expect(wrapper.get('video').element).not.toBe(player);
    } finally {wrapper.unmount();vi.useRealTimers();}
  });

  it('late refresh snapshots and details cannot overwrite a newer refresh or user selection', async () => {
    vi.mocked(isTauri).mockReturnValue(true); vi.mocked(listen).mockResolvedValue(vi.fn());
    const page={items:['e3','e2'].map(reworkEntry),next_cursor:null};
    vi.mocked(api.archiveTimeline).mockResolvedValue(page);
    vi.mocked(api.archiveEntryGet).mockImplementation(async id=>({entry:{body:id},revisions:[],observations:[],media:reworkMedia}));
    const {wrapper}=await setup(false);
    await wrapper.findAll('.entry-copy')[1]!.trigger('click');await flushPromises();
    let finishOld!: (value:typeof page)=>void;
    vi.mocked(api.archiveTimeline).mockReturnValueOnce(new Promise(resolve=>{finishOld=resolve;}));
    vi.useFakeTimers();
    try {
      changeEvent();await vi.advanceTimersByTimeAsync(260);
      let finishDetail!: (value:Awaited<ReturnType<typeof api.archiveEntryGet>>)=>void;
      vi.mocked(api.archiveEntryGet).mockReturnValueOnce(new Promise(resolve=>{finishDetail=resolve;}));
      changeEvent();await vi.advanceTimersByTimeAsync(260);await flushPromises();
      await wrapper.findAll('.entry-copy')[0]!.trigger('click');await flushPromises();
      const player=wrapper.get('video').element as HTMLVideoElement;player.currentTime=11;
      finishOld({items:[],next_cursor:null});
      finishDetail({entry:{body:'stale e2'},revisions:[],observations:[],media:[]});await flushPromises();
      expect(wrapper.get('.detail-title').text()).toBe('e3');
      expect(wrapper.get('.detail-description').text()).toBe('e3');
      expect(wrapper.findAll('.feed-card')).toHaveLength(2);
      expect(wrapper.get('video').element).toBe(player);expect(player.currentTime).toBe(11);
    } finally {wrapper.unmount();vi.useRealTimers();}
  });
});
  it('A review: unrelated archive events preserve the selected entry and player', async () => {
    vi.mocked(isTauri).mockReturnValue(true);
    vi.mocked(listen).mockResolvedValue(vi.fn());
    const items = ['one', 'two'].map(id => ({entry_id:id,current_revision_id:null,title:id,namespace:'bilibili',kind:'video',published_at:null,published_precision:'unknown'}));
    vi.mocked(api.archiveTimeline).mockResolvedValue({items,next_cursor:null});
    vi.mocked(api.archiveEntryGet).mockResolvedValue({entry:{body:'body'},revisions:[],observations:[],media:[{media_id:'m',variant_id:'v',blob_id:'sha256:'+'a'.repeat(64),role:'video',position:0,available:true,mime:'video/mp4',size:128}]});
    const {wrapper}=await setup(false);
    await wrapper.findAll('.entry-copy')[1]!.trigger('click'); await flushPromises();
    expect(wrapper.get('.detail-title').text()).toBe('two');
    const player=wrapper.get('video').element;
    const change=vi.mocked(listen).mock.calls.find(c=>c[0]==='trace://change')?.[1];
    vi.useFakeTimers();
    try {
      change?.({event:'trace://change',id:1,payload:{topic:'creator.updated',aggregate_id:'unrelated'}});
      await vi.advanceTimersByTimeAsync(260); await flushPromises();
      expect(wrapper.get('.detail-title').text()).toBe('two');
      expect(wrapper.get('video').element).toBe(player);
    } finally { wrapper.unmount(); vi.useRealTimers(); }
  });

describe('主页的预览与真实档案边界', () => {
  it('组合筛选不修改原数组，排序只改变结果', () => {
    expect(filterFeed(previewEntries,'新海诚','video','youtube',false).map(x=>x.id)).toEqual(['preview-2']);
    expect(filterFeed(previewEntries,'','video','x',false)).toEqual([]);
    expect(filterFeed(previewEntries,'','','',true)[0]?.id).toBe('preview-4');
    expect(previewEntries[0]?.id).toBe('preview-1');
  });
  it('预览搜索、空结果和卡片详情同步', async () => {
    const {wrapper,store}=await setup();
    store.query='新海诚'; await flushPromises();
    expect(wrapper.findAll('.feed-card')).toHaveLength(1);
    expect(wrapper.find('.detail-title').text()).toContain('新海诚');
    store.query='no-match'; await flushPromises();
    expect(wrapper.findAll('.feed-card')).toHaveLength(0);
    expect(wrapper.find('.detail-empty').exists()).toBe(true);
    expect(api.archiveTimeline).not.toHaveBeenCalled();
    wrapper.unmount();
  });
  it('示例订阅和下载不调用档案 API', async () => {
    const {wrapper}=await setup();
    await wrapper.get('.primary-button').trigger('click');
    expect(wrapper.get('.primary-button').text()).toBe('订阅');
    await wrapper.get('.entry-metrics button').trigger('click');
    expect(api.archiveTimeline).not.toHaveBeenCalled();
    expect(api.archiveEntryGet).not.toHaveBeenCalled();
    wrapper.unmount();
  });
  it('实际空档案没有虚构的数量、作者或已保存文件', async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({items:[],next_cursor:null});
    const {wrapper}=await setup(false);
    expect(wrapper.text()).toContain('还没有收录内容');
    expect(wrapper.text()).not.toContain('1,284');
    expect(wrapper.text()).not.toContain('森中人');
    expect(wrapper.find('.saved-files').exists()).toBe(false);
    wrapper.unmount();
  });
  it('读取失败显示重试，恢复后清除错误', async () => {
    vi.mocked(api.archiveTimeline).mockRejectedValueOnce(new Error('disconnected')).mockResolvedValueOnce({items:[],next_cursor:null});
    const {wrapper}=await setup(false);
    expect(wrapper.find('[role="alert"]').text()).toContain('无法连接档案');
    await wrapper.get('.feed-state button').trigger('click'); await flushPromises();
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    expect(wrapper.text()).toContain('还没有收录内容');
    wrapper.unmount();
  });
  it('真实记录显示实际状态，媒体操作不调用不安全路径 API', async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({items:[{entry_id:'real-1',current_revision_id:null,title:'真实记录',namespace:'bilibili',kind:'video',published_at:null,published_precision:'unknown',first_seen_at:'2026-10-07',save_progress:'partial',quality_state:'unknown'}],next_cursor:null});
    vi.mocked(api.archiveEntryGet).mockResolvedValue({entry:{body:'实际正文'},revisions:[],observations:[]});
    const {wrapper}=await setup(false);
    expect(wrapper.get('.detail-title').text()).toBe('真实记录');
    expect(wrapper.get('.detail-description').text()).toBe('实际正文');
    expect(wrapper.get('.detail-actions button').attributes('disabled')).toBeDefined();
    expect(wrapper.text()).not.toContain('1.2 GB');
    wrapper.unmount();
  });
  it('侧栏是真实路由，后台状态入口保留', async () => {
    const {wrapper,router}=await setup(true,true);
    await wrapper.get('a[href="/creators"]').trigger('click'); await flushPromises();
    expect(router.currentRoute.value.path).toBe('/creators');
    expect(wrapper.text()).toContain('订阅管理页');
    await wrapper.get('a[aria-label="后台状态"]').trigger('click'); await flushPromises();
    expect(router.currentRoute.value.path).toBe('/status');
    wrapper.unmount();
  });
  it('晚到的旧请求不能覆盖新的搜索结果', async () => {
    let completeOld!: (value: {items: never[]; next_cursor: null}) => void;
    vi.mocked(api.archiveTimeline).mockReturnValueOnce(new Promise(resolve => { completeOld = resolve; }));
    vi.mocked(api.archiveTimeline).mockResolvedValueOnce({items:[{entry_id:'new',current_revision_id:null,title:'新的搜索结果',namespace:'bilibili',kind:'video',published_at:null,published_precision:'unknown',first_seen_at:'2026-10-07'}],next_cursor:null});
    vi.mocked(api.archiveEntryGet).mockResolvedValue({entry:{},revisions:[],observations:[]});
    const {wrapper,store}=await setup(false);
    vi.useFakeTimers();
    try {
      store.query='新'; await flushPromises();
      await vi.advanceTimersByTimeAsync(250); await flushPromises();
      completeOld({items:[],next_cursor:null}); await flushPromises();
      expect(wrapper.get('.entry-copy').text()).toContain('新的搜索结果');
    } finally { wrapper.unmount(); vi.useRealTimers(); }
  });
  it('真实订阅失败保留持久状态，成功后重新读取并支持账号组合查询', async () => {
    vi.mocked(api.creatorList).mockResolvedValue({items:[{creator_id:'c',display_name:'很长的中文创作者名称用于验证换行'.repeat(3)}],next_cursor:null});
    vi.mocked(api.accountList).mockResolvedValue({items:[{account_id:'a',creator_id:'c',namespace:'bilibili',native_account_key:'1',display_name:'合成账号',profile_url:null}],next_cursor:null});
    const active={subscription_id:'s',account_id:'a',content_type:'video',state:'enabled',range:{},check_interval_seconds:300,next_check_at:null};
    vi.mocked(api.subscriptionList).mockResolvedValue({items:[active],next_cursor:null});
    vi.mocked(api.archiveTimeline).mockResolvedValue({items:[],next_cursor:null,counts:{global:65,filtered:0,scope:'all-matching-entries'}});
    vi.mocked(api.subscriptionSetEnabled).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ok:true});
    const {wrapper}=await setup(false);
    await wrapper.get('select[aria-label="选择创作者"]').setValue('c'); await flushPromises();
    await wrapper.get('select[aria-label="选择来源账号"]').setValue('a');
    expect(wrapper.get('.subscribe-actions .primary-button').text()).toBe('暂停订阅');
    await wrapper.get('.subscribe-actions .primary-button').trigger('click'); await flushPromises();
    expect(wrapper.get('.subscribe-actions .primary-button').text()).toBe('暂停订阅');
    vi.mocked(api.subscriptionList).mockResolvedValue({items:[{...active,state:'paused'}],next_cursor:null});
    await wrapper.get('.subscribe-actions .primary-button').trigger('click'); await flushPromises();
    expect(wrapper.get('.subscribe-actions .primary-button').text()).toBe('恢复订阅');
    expect(api.subscriptionSetEnabled).toHaveBeenLastCalledWith('s',false);
    await new Promise(resolve=>setTimeout(resolve,300)); await flushPromises();
    expect(api.archiveTimeline).toHaveBeenLastCalledWith(expect.objectContaining({creatorId:'c',accountId:'a'}));
    expect(wrapper.text()).toContain('全档案内容65');
    wrapper.unmount();
  });
  it('卸载后的详情请求不更新页面或发起新写操作', async () => {
    let resolveDetail!: (value:{entry:unknown;revisions:never[];observations:never[]})=>void;
    vi.mocked(api.archiveTimeline).mockResolvedValue({items:[{entry_id:'r',current_revision_id:null,title:'中文长标题'.repeat(30),namespace:'bilibili',kind:'video',published_at:null,published_precision:'unknown'}],next_cursor:null});
    vi.mocked(api.archiveEntryGet).mockReturnValue(new Promise(resolve=>{resolveDetail=resolve;}));
    const {wrapper}=await setup(false);
    expect(wrapper.get('.entry-copy').attributes('aria-label')).toContain('中文长标题');
    wrapper.unmount(); resolveDetail({entry:{body:'late'},revisions:[],observations:[]}); await flushPromises();
    expect(api.subscriptionCreate).not.toHaveBeenCalled();
  });
  it('真实侧栏只提供当前平台，预览平台不成为生产入口', async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({items:[],next_cursor:null});
    const {wrapper}=await setup(false,true);
    expect(wrapper.findAll('.platform-nav')).toHaveLength(1);
    expect(wrapper.get('.platform-nav').text()).toContain('Bilibili');
    expect(wrapper.text()).not.toContain('直播录制');
    wrapper.unmount();
  });
  it('断连提示和重连快照事件实际刷新，卸载释放监听', async () => {
    vi.mocked(isTauri).mockReturnValue(true);
    const stop=vi.fn(); vi.mocked(listen).mockResolvedValue(stop);
    vi.mocked(api.archiveTimeline).mockResolvedValue({items:[],next_cursor:null});
    const {wrapper}=await setup(false);
    const disconnect=vi.mocked(listen).mock.calls.find(c=>c[0]==='trace://disconnected')?.[1];
    const resync=vi.mocked(listen).mock.calls.find(c=>c[0]==='trace://resync')?.[1];
    expect(disconnect).toBeDefined(); expect(resync).toBeDefined();
    disconnect?.({event:'trace://disconnected',id:1,payload:{}}); await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain('后台连接已断开');
    vi.useFakeTimers();
    try {
      resync?.({event:'trace://resync',id:1,payload:{}});
      await vi.advanceTimersByTimeAsync(260); await flushPromises();
      expect(api.archiveTimeline).toHaveBeenCalledTimes(2);
      expect(wrapper.find('[role="alert"]').exists()).toBe(false);
      wrapper.unmount(); expect(stop).toHaveBeenCalledTimes(3);
    } finally { vi.useRealTimers(); }
  });

});
