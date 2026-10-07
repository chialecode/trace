<script setup lang="ts">
import {computed,onMounted,onBeforeUnmount,ref,watch} from 'vue';
import {NButton,NAlert,NSpin} from 'naive-ui';
import {api,type SourcePreview,type SourceOffer,type SourceRequest,type CredentialDto} from '../lib/api';
import {useWorkspaceStore} from '../stores/workspace';
const workspace=useWorkspaceStore();
const url=ref('');const credential=ref('');const credentials=ref<CredentialDto[]>([]);
const preview=ref<SourcePreview|null>(null);const selected=ref<Record<string,string>>({});
const requests=ref<SourceRequest[]>([]);const resolving=ref(false);const submitting=ref(false);
const message=ref('');let epoch=0;let timer:ReturnType<typeof setTimeout>|undefined;let closed=false;
const previewFailed=ref(false);
const site=computed(()=>{try{return new URL(url.value).hostname.includes('bili')||new URL(url.value).hostname==='b23.tv'?'bilibili':'douyin';}catch{return '';}});
const availableCredentials=computed(()=>credentials.value.filter(c=>c.platform===site.value));
const groups=computed(()=>{
  const groups=new Map<string,SourceOffer[]>();for(const o of preview.value?.offers??[]){const list=groups.get(o.attachment_id)??[];list.push(o);groups.set(o.attachment_id,list);}return [...groups];
});
watch([url,credential],()=>{epoch++;preview.value=null;selected.value={};message.value='';previewFailed.value=false;resolving.value=false;});
function previewFailure(error:unknown){
  const value=String(error);const reasons=['该房间当前未开播，请等待开播或选择另一个直播链接','来源当前没有可用资源，请检查来源状态或稍后重试','该来源格式或传输方式当前不受支持','检查该账号的来源访问权限','重新登录该平台账号','检查平台模块状态与更新'];
  const reason=reasons.find(r=>value.includes(r));
  return reason?`无法取得可用选项：${reason}。已保存内容保持可读。`:'无法取得该链接的可用选项。请检查来源链接、登录状态或访问权限；后台保留已保存内容。';
}
function label(o:SourceOffer){
  if(o.role==='live')return `${o.quality} · ${o.protocol?.toUpperCase()} · 录制后核实实际尺寸`;
  const s=o.spec??{};const dims=s.width&&s.height?`${s.width} × ${s.height}`:'尺寸未提供';
  return `${dims} · ${s.codec??s.codecs??'编码未提供'} · ${s.fps??s.frame_rate??'帧率未知'} · ${o.segments?.length??1} 段${o.bytes?` · ${(o.bytes/1048576).toFixed(1)} MiB`:''}`;
}
const roles:Record<string,string>={video:'视频',audio:'必要音轨',cover:'封面',live:'直播流',subtitle:'字幕',motion:'实况片段',music:'配乐'};
async function refresh(){if(workspace.preview||closed)return;try{requests.value=(await api.sourceList()).items;}catch{message.value='暂时无法连接后台；任务状态将在重连后更新。';}}
async function resolve(){
  if(workspace.preview||resolving.value||submitting.value)return;
  const mine=++epoch;resolving.value=true;message.value='';previewFailed.value=false;preview.value=null;
  try{
    const result=await api.sourcePreview({url:url.value,credential_id:credential.value||null});
    if(mine!==epoch||closed)return;preview.value=result;
    const choices:Record<string,string>={};for(const o of result.offers){if(!choices[o.attachment_id])choices[o.attachment_id]=o.id;}selected.value=choices;
  }catch(error){if(mine===epoch){message.value=previewFailure(error);previewFailed.value=true;}}
  finally{if(mine===epoch)resolving.value=false;}
}
async function confirm(){
  if(workspace.preview||!preview.value||submitting.value)return;
  const mine=epoch;const choice=preview.value;submitting.value=true;message.value='';
  try{await api.sourceConfirm({preview_id:choice.preview_id,selected:Object.values(selected.value).filter(Boolean)});if(mine===epoch){message.value='已创建一次性任务。关闭窗口后后台继续执行；已有资源的替换需在资源升级中确认。';preview.value=null;}await refresh();}
  catch{if(mine===epoch)message.value='选择未被接受：来源选项、预览时间或登录身份可能已变化。请重新解析并确认选择。';}
  finally{submitting.value=false;}
}
async function stop(r:SourceRequest){if(workspace.preview)return;try{if(r.session_id&&r.session_generation!=null)await api.liveSetRecording(r.session_id,false,r.session_generation);else await api.sourceCancel(r.request_id);await refresh();}catch{message.value='任务状态已变化，请刷新后重试。';}}
onMounted(async()=>{if(workspace.preview)return;try{credentials.value=(await api.credentialList()).items;}catch{message.value='无法读取登录身份。';}await refresh();const poll=async()=>{await refresh();if(!closed)timer=setTimeout(poll,3000);};timer=setTimeout(poll,3000);});
onBeforeUnmount(()=>{closed=true;epoch++;if(timer)clearTimeout(timer);});
const states:Record<string,string>={preview:'待选择',queued:'等待执行',leased:'准备执行',running:'执行中',waiting:'等待录制',recording:'录制中',reconnecting:'重连中',stopping:'正在封片',sealed:'已封片',succeeded:'任务完成',needs_attention:'需要处理',cancelled:'已取消',failed:'失败',waiting_auth:'访问身份受限',waiting_retry:'等待重试',waiting_storage:'等待可用空间'};
const phases:Record<string,string>={source_acquire:'核对所选来源',media:'保存资源',record:'直播录制'};
const sites:Record<string,string>={bilibili:'哔哩哔哩',douyin:'抖音'};
</script>
<template>
  <section class="source-acquisition">
    <h1>链接归档</h1><p>粘贴哔哩哔哩或抖音的视频、直播链接，选择本次可得的资源。直播录制目标至少 720P，实际规格在封片后显示。</p>
    <n-alert v-if="workspace.preview" type="info">界面预览不访问真实来源。请在桌面应用中使用链接归档。</n-alert>
    <form @submit.prevent="resolve">
      <label>视频或直播链接<input v-model="url" aria-label="视频或直播链接" placeholder="https://…" :disabled="submitting||workspace.preview" required /></label>
      <label>获取身份<select v-model="credential" aria-label="获取身份" :disabled="submitting||workspace.preview"><option value="">未登录</option><option v-for="c in availableCredentials" :key="c.credential_id" :value="c.credential_id">{{c.subject||c.credential_id}} · {{c.status==='valid'||c.status==='authenticated'?'已验证':c.status==='invalid'?'已失效':'待验证'}}</option></select></label>
      <n-button attr-type="submit" type="primary" :disabled="!url||workspace.preview||submitting" :loading="resolving">解析可得选项</n-button>
      <router-link to="/accounts">管理登录身份</router-link>
    </form>
    <p v-if="message" role="status" :data-preview-failed="previewFailed?'true':undefined">{{message}}</p>
    <n-spin :show="resolving"><article v-if="preview" class="source-preview">
      <h2>{{preview.title}}</h2><p>{{sites[preview.platform]||preview.platform}} · {{preview.account_name||'来源作者'}} · {{preview.authentication==='authenticated'?'登录获取':'未登录获取'}}</p>
      <p>{{preview.kind==='live'?'已识别真实房间':'已识别作品'}}：{{preview.native_key}} · {{preview.source_url}}</p>
      <p>选项有效至 {{new Date(preview.expires_at).toLocaleTimeString()}}；过期或必要资源变化时需重新解析。</p>
      <label v-for="[resource,offers] in groups" :key="resource" class="source-choice">{{roles[offers[0]!.role]||offers[0]!.role}} · {{offers[0]!.title||(offers[0]!.position?`第 ${offers[0]!.position} 项`:'')}}<select v-model="selected[resource]" :aria-label="`选择${roles[offers[0]!.role]||offers[0]!.role} ${resource}`" :disabled="submitting"><option v-if="!offers.some(o=>o.required)" value="">本次不保存</option><option v-for="o in offers" :key="o.id" :value="o.id">{{label(o)}}</option></select></label>
      <p v-if="preview.kind==='video'">每项选择包含其全部必要分段；已有资源仍可读取，升级候选等待确认。</p>
      <n-button type="primary" :loading="submitting" :disabled="submitting" @click="confirm">{{preview.kind==='live'?'开始录制':'下载所选资源'}}</n-button>
    </article></n-spin>
    <div class="source-heading"><h2>最近的一次性任务</h2><n-button @click="refresh" :disabled="workspace.preview">刷新任务</n-button></div>
    <p v-if="!requests.length">还没有一次性归档任务。</p>
    <article v-for="r in requests" :key="r.request_id" :data-source-request="r.request_id" class="source-task"><h3>{{r.title}}</h3><p>{{sites[r.platform]||r.platform}} · {{states[r.state]||r.state}}<span v-if="r.session_state"> · {{states[r.session_state]||r.session_state}}</span></p><ul><li v-for="j in r.tasks" :key="j.job_id">{{phases[j.kind]||j.kind}} · {{states[j.state]||j.state}}<span v-if="j.error"> · 需要查看任务详情或重新解析来源</span></li></ul><div class="source-actions"><n-button v-if="r.state==='running'" @click="stop(r)">{{r.kind==='live'?'停止并封片':'取消任务'}}</n-button><router-link v-if="r.entry_id" :to="{path:'/',query:{entry:r.entry_id}}">读取档案</router-link><router-link v-if="r.entry_id" to="/storage">导出档案</router-link><router-link to="/tasks">任务详情</router-link></div></article>
  </section>
</template>
<style scoped>
.source-acquisition{max-width:1040px;margin:auto;color:var(--text-primary,#203657)}h1{font-size:24px}p{line-height:1.7;color:#526883}form{display:flex;align-items:end;gap:14px;flex-wrap:wrap;margin:24px 0}label{display:flex;flex-direction:column;gap:7px;font-size:14px}form label:first-child{flex:1;min-width:260px}input,select{min-height:38px;border:1px solid #d7e3f3;border-radius:8px;background:white;padding:8px;color:inherit;max-width:100%}input:focus,select:focus{outline:2px solid #3979ff;outline-offset:2px}.source-preview,.source-task{background:#fff;border:1px solid #e6eefb;border-radius:9px;padding:20px;margin:18px 0}.source-choice{margin:16px 0;overflow-wrap:anywhere}.source-heading,.source-actions{display:flex;gap:18px;align-items:center;flex-wrap:wrap}.source-task li{margin:7px 0;font-size:13px}a{color:#3979ff}@media(max-width:760px){form{align-items:stretch;flex-direction:column}.source-preview,.source-task{padding:14px}}
</style>
