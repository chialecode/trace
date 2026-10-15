import {mount,flushPromises} from '@vue/test-utils';
import {defineComponent} from 'vue';
import {NMessageProvider} from 'naive-ui';
import {it,expect,vi} from 'vitest';
import ModulesView from '../ModulesView.vue';
import {api,type PluginDto} from '../../lib/api';

vi.mock('../../lib/api',async original=>{const source=await original<typeof import('../../lib/api')>();return {...source,api:{...source.api,pluginList:vi.fn(),pluginSetState:vi.fn()}};});
it('c08_module_stop_shows_draining_until_workers_finish',async()=>{
  const row=(state:string,count=0):PluginDto=>({plugin_id:'bilibili',platform:'bilibili',state,active_tasks:count});
  vi.mocked(api.pluginList).mockResolvedValue({items:[row('installed',1)],next_cursor:null});
  let finish!:(value:unknown)=>void;
  vi.mocked(api.pluginSetState).mockImplementation(()=>new Promise(resolve=>finish=resolve));
  const wrapper=mount(defineComponent({components:{NMessageProvider,ModulesView},template:'<NMessageProvider><ModulesView/></NMessageProvider>'}));
  await flushPromises();vi.useFakeTimers();
  try {
    expect(wrapper.text()).toContain('已安装并启用');
    await wrapper.findAll('button').find(button=>button.text()==='停用')!.trigger('click');
    expect(api.pluginSetState).toHaveBeenCalledWith('bilibili',false);
    expect(wrapper.text()).toContain('正在停止调度');
    vi.mocked(api.pluginList).mockResolvedValue({items:[row('stopping',1)],next_cursor:null});
    finish({ok:true});await flushPromises();
    expect(wrapper.text()).toContain('安全收尾中');expect(wrapper.text()).toContain('1 项任务');
    expect(wrapper.findAll('button').find(button=>button.text()==='正在收尾')!.attributes('disabled')).toBeDefined();
    vi.mocked(api.pluginList).mockResolvedValue({items:[row('disabled')],next_cursor:null});
    await wrapper.findAll('button').find(button=>button.text()==='刷新')!.trigger('click');await flushPromises();
    expect(wrapper.text()).toContain('已停用');expect(wrapper.text()).toContain('0 项任务');
    expect(wrapper.findAll('button').find(button=>button.text()==='启用')!.attributes('disabled')).toBeUndefined();
  } finally {wrapper.unmount();vi.useRealTimers();}
});
