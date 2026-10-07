import {beforeEach,it,expect,vi} from 'vitest';
import {mount,flushPromises} from '@vue/test-utils';
import {createPinia,setActivePinia} from 'pinia';
import {createRouter,createMemoryHistory} from 'vue-router';
import AcquireView from '../AcquireView.vue';
import {api,type SourcePreview} from '../../lib/api';
import {useWorkspaceStore} from '../../stores/workspace';
vi.mock('../../lib/api',async original=>{const m=await original<typeof import('../../lib/api')>();return {...m,api:{...m.api,credentialList:vi.fn(),sourceList:vi.fn(),sourcePreview:vi.fn(),sourceConfirm:vi.fn(),sourceCancel:vi.fn(),liveSetRecording:vi.fn()}};});
const sample=(id='preview'):SourcePreview=>({preview_id:id,kind:'video',platform:'bilibili',source_url:'https://www.bilibili.com/video/BV1Ab4C5D6E7',native_key:'BV1Ab4C5D6E7',title:'明确选择',authentication:'authenticated',expires_at:'2030-01-01T00:00:00Z',offers:[{id:'4k',role:'video',attachment_id:'video',required:true,spec:{width:3840,height:2160,codec:'avc1'},segments:[{},{}]},{id:'720',role:'video',attachment_id:'video',required:true,spec:{width:1280,height:720,codec:'avc1'}},{id:'aac',role:'audio',attachment_id:'audio',required:true,spec:{codec:'mp4a'}}]});
beforeEach(()=>{vi.resetAllMocks();vi.mocked(api.credentialList).mockResolvedValue({items:[],next_cursor:null});vi.mocked(api.sourceList).mockResolvedValue({items:[]});});
async function page(preview=false){const pinia=createPinia();setActivePinia(pinia);useWorkspaceStore().preview=preview;const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component:AcquireView}]});await router.push('/');const w=mount(AcquireView,{global:{plugins:[pinia,router]}});await flushPromises();return w;}
it('c14_link_ui_requires_choice_keeps_audio_and_rejects_stale_double_submit',async()=>{
  vi.mocked(api.sourcePreview).mockResolvedValue(sample());const w=await page();await w.get('input').setValue(sample().source_url);await w.get('form').trigger('submit');await flushPromises();
  expect(api.sourceConfirm).not.toHaveBeenCalled();expect(w.text()).toContain('3840 × 2160');expect(w.text()).toContain('2 段');expect(w.text()).toContain('必要音轨');
  await w.get('select[aria-label="选择视频 video"]').setValue('720');
  let reject!:(e:Error)=>void;vi.mocked(api.sourceConfirm).mockImplementation(()=>new Promise((_,fail)=>reject=fail));
  const download=w.findAll('button').find(b=>b.text()==='下载所选资源')!;await download.trigger('click');await download.trigger('click');expect(api.sourceConfirm).toHaveBeenCalledTimes(1);expect(api.sourceConfirm).toHaveBeenCalledWith({preview_id:'preview',selected:['720','aac']});
  reject(Error('version_conflict'));await flushPromises();expect(w.text()).toContain('重新解析并确认选择');expect((w.get('select[aria-label="选择视频 video"]').element as HTMLSelectElement).value).toBe('720');w.unmount();
});
it('c14_link_ui_ignores_late_preview_after_source_or_identity_change',async()=>{
  let finish!:(v:SourcePreview)=>void;vi.mocked(api.sourcePreview).mockImplementationOnce(()=>new Promise(done=>finish=done));const w=await page();await w.get('input').setValue(sample().source_url);await w.get('form').trigger('submit');await w.get('input').setValue('https://www.douyin.com/video/7000000000000000001');finish(sample('old'));await flushPromises();expect(w.find('.source-preview').exists()).toBe(false);expect(api.sourceConfirm).not.toHaveBeenCalled();w.unmount();
});
it('c14_link_ui_stops_core_recording_and_demo_has_zero_calls',async()=>{
  vi.mocked(api.sourceList).mockResolvedValue({items:[{request_id:'record',platform:'douyin',kind:'live',state:'running',session_id:'session',session_generation:3,title:'正在录制',authentication:'authenticated',tasks:[]}]});const w=await page();await w.findAll('button').find(b=>b.text()==='停止并封片')!.trigger('click');await flushPromises();expect(api.liveSetRecording).toHaveBeenCalledWith('session',false,3);expect(api.sourceCancel).not.toHaveBeenCalled();w.unmount();vi.clearAllMocks();const demo=await page(true);expect(api.sourceList).not.toHaveBeenCalled();expect(api.credentialList).not.toHaveBeenCalled();expect(api.sourcePreview).not.toHaveBeenCalled();demo.unmount();
});
it('c14_link_ui_distinguishes_offline_denied_and_unsupported_previews',async()=>{
  for(const reason of ['该房间当前未开播，请等待开播或选择另一个直播链接','检查该账号的来源访问权限','该来源格式或传输方式当前不受支持']){
    vi.mocked(api.sourcePreview).mockRejectedValueOnce(`invalid input: Internal: ${reason}`);const w=await page();await w.get('input').setValue('https://live.douyin.com/700000001');await w.get('form').trigger('submit');await flushPromises();expect(w.get('[data-preview-failed=true]').text()).toContain(reason);expect(w.find('.source-preview').exists()).toBe(false);expect(api.sourceConfirm).not.toHaveBeenCalled();w.unmount();
  }
});
