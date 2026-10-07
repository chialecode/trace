import { mount,flushPromises } from '@vue/test-utils';
import { beforeEach,it,expect,vi } from 'vitest';
import ArchiveDocument from '../ArchiveDocument.vue';
import LiveRecording from '../LiveRecording.vue';
import LiveChat from '../LiveChat.vue';
import ArchiveHistory from '../ArchiveHistory.vue';
import AccountHistory from '../AccountHistory.vue';
import { api,type FragmentDto,type RevisionDto } from '../../lib/api';
import { readRevision } from '../../lib/archive';
vi.mock('@tauri-apps/api/core',()=>({invoke:vi.fn()}));
vi.mock('../../lib/api',async(importOriginal)=>{const original=await importOriginal<typeof import('../../lib/api')>();return {...original,api:{...original.api,liveAuxiliary:vi.fn(),liveFragments:vi.fn(),liveSetRecording:vi.fn(),openExternal:vi.fn(),archiveEntryPage:vi.fn(),archiveRevisionRead:vi.fn(),accountHistory:vi.fn(),accountResolve:vi.fn()}};});
beforeEach(()=>vi.resetAllMocks());
it('c08_live_chat_offline_pagination_inert_text_and_stale_session',async()=>{
  const item=(sequence:number)=>({sequence,source_id:String(sequence),text:'<img src="https://invalid/pixel">',author:'甲',observed_at:'t'});
  vi.mocked(api.liveAuxiliary).mockResolvedValueOnce({items:[item(31)],total:31,next_before:31,enabled:true,latest:{coverage:'sampled_history',omitted:1,observed_at:'t'}}).mockResolvedValueOnce({items:[item(1)],total:31,enabled:true});
  const wrapper=mount(LiveChat,{props:{sessionId:'s1'}});expect(api.liveAuxiliary).not.toHaveBeenCalled();
  (wrapper.get('details').element as HTMLDetailsElement).open=true;await wrapper.get('details').trigger('toggle');await flushPromises();
  expect(wrapper.text()).toContain('有限历史窗口');expect(wrapper.findAll('img,script,iframe')).toHaveLength(0);
  await wrapper.findAll('button').find(b=>b.text()==='加载更早弹幕')!.trigger('click');await flushPromises();expect(wrapper.findAll('li')).toHaveLength(2);expect(api.liveAuxiliary).toHaveBeenLastCalledWith('s1',31);
  let finish!:()=>void;vi.mocked(api.liveAuxiliary).mockImplementationOnce(()=>new Promise(resolve=>{finish=()=>resolve({items:[item(99)],total:99,enabled:true});}));
  await wrapper.findAll('button').find(b=>b.text()==='刷新弹幕')!.trigger('click');await wrapper.setProps({sessionId:'s2'});finish();await flushPromises();
  expect(wrapper.findAll('li')).toHaveLength(0);expect(wrapper.text()).not.toContain('99');wrapper.unmount();
});
const live={session_id:'s1',state:'recording',generation:3,manual_stopped:false,started_at:'t'};
const fragment=(sequence:number):FragmentDto=>({sequence,state:'sealed',blob_id:'sha256:'+'a'.repeat(64),available:true});
it('c08_document_resource_ui_races_and_live_controls',async()=>{
  const doc=mount(ArchiveDocument,{props:{body:'',media:[],document:{version:1,omissions:[],blocks:[{kind:'text',style:'h2',text:'离线标题'},{kind:'text',style:'paragraph',text:'<script>malicious()</script>'},{kind:'resource',attachment_id:'not-saved',caption:'缺失原图'},{kind:'link',text:'外部内容',url:'https://tracking.invalid/pixel'}]}}});
  expect(doc.get('h2').text()).toBe('离线标题');expect(doc.findAll('script,iframe,a,img')).toHaveLength(0);expect(doc.text()).toContain('缺失原图 · 未保存');doc.unmount();
  vi.mocked(api.liveFragments).mockResolvedValueOnce({items:[fragment(4),fragment(3)],next_before:3,total:4}).mockResolvedValueOnce({items:[fragment(2),fragment(1)],total:4}).mockResolvedValueOnce({items:[fragment(5),fragment(4)],next_before:4,total:5}).mockResolvedValueOnce({items:[fragment(3),fragment(2),{...fragment(1),state:'superseded'}],total:5});
  const wrapper=mount(LiveRecording,{props:{live,fragments:[]}});
  const details=wrapper.get('details').element as HTMLDetailsElement;details.open=true;await wrapper.get('details').trigger('toggle');await flushPromises();
  await wrapper.findAll('button').find(b=>b.text()==='加载更早分片')!.trigger('click');await flushPromises();expect(wrapper.findAll('li')).toHaveLength(4);
  await wrapper.findAll('button').find(b=>b.text()==='刷新分片')!.trigger('click');await flushPromises();
  expect(wrapper.findAll('li')).toHaveLength(5);expect(wrapper.text()).toContain('已重新取得');expect(wrapper.text()).toContain('已载入 5 / 5 项');
  await wrapper.findAll('button').find(b=>b.text()==='打开已保存分片')!.trigger('click');expect(api.openExternal).toHaveBeenCalledWith('sha256:'+'a'.repeat(64));
  await wrapper.findAll('button').find(b=>b.text()==='停止本场录制')!.trigger('click');await flushPromises();expect(api.liveSetRecording).toHaveBeenCalledWith('s1',false,3);expect(wrapper.emitted('refresh')).toHaveLength(1);
  let finish!:(value:{items:FragmentDto[];total:number})=>void;
  vi.mocked(api.liveFragments).mockImplementationOnce(()=>new Promise(resolve=>{finish=resolve;}));
  await wrapper.findAll('button').find(b=>b.text()==='刷新分片')!.trigger('click');await wrapper.setProps({live:{...live,session_id:'s2',manual_stopped:true}});finish({items:[fragment(100)],total:1});await flushPromises();expect(wrapper.findAll('li')).toHaveLength(0);expect(wrapper.text()).toContain('已手动停止本场');wrapper.unmount();
});
it('reads every Unicode chunk and abandons a stale full-document request',async()=>{
  const revision:RevisionDto={revision_id:'r',completeness:'full',content_inline:false};
  vi.mocked(api.archiveRevisionRead).mockImplementation(async(_e,_r,field,offset)=>field==='body'?{text:offset===0?'甲😀':'乙',next_offset:offset===0?2:null,total_chars:3}:{text:field==='document'?'{}':'{"id":"90071992547409931"}',next_offset:null,total_chars:2});
  const full=await readRevision('e',revision);expect(full.body).toBe('甲😀乙');expect(full.metadata?.id).toBe('90071992547409931');expect(api.archiveRevisionRead).toHaveBeenCalledWith('e','r','body',2);
  await expect(readRevision('e',revision,()=>false)).rejects.toThrow('stale archive request');
});
it('history bodies are lazy and an inline historical revision remains readable',async()=>{
  const revisions:RevisionDto[]=[{revision_id:'r1',title:'旧版本',completeness:'full',content_inline:false},{revision_id:'r2',title:'新版本',completeness:'full',content_inline:true,body:'完整新正文'}];
  vi.mocked(api.archiveEntryPage).mockResolvedValueOnce({items:revisions,next_cursor:null}).mockResolvedValue({items:[],next_cursor:null});
  const wrapper=mount(ArchiveHistory,{props:{entryId:'e'}});expect(api.archiveEntryPage).not.toHaveBeenCalled();
  await wrapper.findAll('button').find(b=>b.text()==='读取版本')!.trigger('click');await flushPromises();
  await wrapper.findAll('button').find(b=>b.text().includes('新版本'))!.trigger('click');await flushPromises();expect(wrapper.text()).toContain('完整新正文');expect(api.archiveRevisionRead).not.toHaveBeenCalled();wrapper.unmount();
});

it('c08_account_history_pages_and_source_refresh_reject_old_account_results',async()=>{
  const account={account_id:'a',creator_id:'c',namespace:'douyin',native_account_key:'MS4test',display_name:'当前昵称',profile_url:null};
  const page=(name:string,sequence:number)=>({sequence,display_name:name,observed_at:'t',evidence:{uid:'90071992547409931'}});
  vi.mocked(api.accountHistory).mockResolvedValueOnce({items:[page('新昵称',5)],next_before:5}).mockResolvedValueOnce({items:[page('旧昵称',1)]});
  const wrapper=mount(AccountHistory,{props:{account}});expect(api.accountHistory).not.toHaveBeenCalled();
  (wrapper.get('details').element as HTMLDetailsElement).open=true;await wrapper.get('details').trigger('toggle');await flushPromises();
  await wrapper.findAll('button').find(b=>b.text()==='更早资料')!.trigger('click');await flushPromises();expect(wrapper.text()).toContain('新昵称');expect(wrapper.text()).toContain('旧昵称');expect(api.accountHistory).toHaveBeenLastCalledWith('a',5);
  let finish!:()=>void;vi.mocked(api.accountResolve).mockImplementationOnce(()=>new Promise(resolve=>{finish=()=>resolve({account_id:'a',native_account_key:'MS4test',display_name:'迟到账号'});}));
  await wrapper.findAll('button').find(b=>b.text()==='从来源更新资料')!.trigger('click');
  vi.mocked(api.accountHistory).mockResolvedValueOnce({items:[page('另一账号',9)]});await wrapper.setProps({account:{...account,account_id:'b',native_account_key:'MS4next'}});await flushPromises();finish();await flushPromises();
  expect(wrapper.text()).toContain('另一账号');expect(wrapper.text()).not.toContain('迟到账号');expect(wrapper.text()).not.toContain('旧昵称');expect(wrapper.emitted('updated')).toBeUndefined();wrapper.unmount();
});

it('c02_document_lists_tables_and_quotes_render_as_inert_local_semantics',()=>{
  const wrapper=mount(ArchiveDocument,{props:{body:'',media:[],document:{version:1,omissions:[],blocks:[{kind:'list_item',text:'第七项',depth:1,ordinal:7},{kind:'list_item',text:'内层项目',depth:2},{kind:'table',header:true,rows:[['字段','值'],['HTML','<img src=https://invalid/pixel>']]},{kind:'text',style:'quote',text:'引用'},{kind:'text',style:'code',text:'<script>source</script>'}]}}});
  expect(wrapper.get('ol').attributes('start')).toBe('7');expect(wrapper.get('ul li').text()).toBe('内层项目');expect(wrapper.findAll('th')).toHaveLength(2);expect(wrapper.get('blockquote').text()).toBe('引用');expect(wrapper.get('pre').text()).toContain('<script>');expect(wrapper.findAll('img,script,iframe')).toHaveLength(0);wrapper.unmount();
});


it('c08_rich_article_text_is_inert_and_preserves_inline_format',()=>{
  const wrapper=mount(ArchiveDocument,{props:{body:'',media:[],document:{version:1,omissions:[],blocks:[
    {kind:'rich_text',style:'h2',depth:1,spans:[{text:'标题',bold:false,italic:false,strike:false}]},
    {kind:'rich_text',style:'ordered',depth:2,ordinal:7,spans:[{text:'<img src="https://invalid/pixel">',bold:true,italic:true,strike:true}]}
  ]}}});
  expect(wrapper.find('h2').text()).toBe('标题');
  expect(wrapper.find('ol').attributes('start')).toBe('7');
  expect(wrapper.find('li').text()).toContain('<img src=');
  expect(wrapper.find('.inline-bold.inline-italic.inline-strike').exists()).toBe(true);
  expect(wrapper.findAll('img')).toHaveLength(0);
});
