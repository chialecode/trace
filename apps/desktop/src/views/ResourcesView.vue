<script setup lang="ts">
import {onMounted,onBeforeUnmount,ref} from 'vue';
import {api,type ResourceDto,type CreatorDto} from '../lib/api';
import {formatBytes,formatTime,mediaRoleLabel} from '../lib/format';
import MediaBrowser from '../components/MediaBrowser.vue';
import {authLabel} from '../lib/acquisition';
const items=ref<ResourceDto[]>([]),creators=ref<CreatorDto[]>([]),selected=ref<ResourceDto>();
const namespace=ref(''),role=ref(''),creator=ref(''),availability=ref('');
const next=ref<string|null>(null),busy=ref(false),error=ref('');let generation=0;
async function load(more=false){
  const request=++generation;busy.value=true;error.value='';
  try{const page=await api.archiveResources({cursor:more?next.value??undefined:undefined,namespace:namespace.value||undefined,role:role.value||undefined,creator_id:creator.value||undefined,available:availability.value?availability.value==='saved':undefined});
    if(request!==generation)return;
    items.value=more?[...items.value,...page.items]:page.items;next.value=page.next_cursor;
    if(!more)selected.value=undefined;
  }catch{if(request===generation)error.value='资源列表未能读取，请重试。';}
  finally{if(request===generation)busy.value=false;}
}
function spec(value?:string|null){try{const s=JSON.parse(value??'null');return s?[s.width&&s.height?`${s.width} × ${s.height}`:'',s.fps?`${s.fps} fps`:'',s.video_codec??s.codec??'',s.duration_ms?`${Math.round(s.duration_ms/1000)} 秒`:''].filter(Boolean).join(' · ')||'规格未知':'规格未知';}catch{return '规格未知';}}
onMounted(()=>{void load();void api.creatorList().then(p=>creators.value=p.items).catch(()=>{});});
onBeforeUnmount(()=>generation++);
</script>
<template>
  <section class="resource-library" aria-label="独立资源库">
    <h2>资源库</h2><p>按发布记录保留每份资源及其版本。相同文件可以由多条来源共同引用；规格未知或副本缺失会明确显示。</p>
    <form class="filters" @submit.prevent="load()">
      <label>平台<select v-model="namespace"><option value="">全部</option><option value="bilibili">哔哩哔哩</option><option value="douyin">抖音</option></select></label>
      <label>类型<select v-model="role"><option value="">全部</option><option value="image">图片</option><option value="cover">封面</option><option value="video">视频</option><option value="audio">音频</option><option value="motion">实况</option><option value="subtitle">字幕</option><option value="recording">录制</option><option value="recording_source">录制原始数据</option></select></label>
      <label>创作者<select v-model="creator"><option value="">全部</option><option v-for="c in creators" :key="c.creator_id" :value="c.creator_id">{{c.display_name}}</option></select></label>
      <label>副本<select v-model="availability"><option value="">全部</option><option value="saved">本地可用</option><option value="missing">未保存或缺失</option></select></label>
      <button :disabled="busy">筛选与刷新</button>
    </form>
    <p v-if="error" role="alert">{{error}}</p><p v-if="!busy&&!items.length&&!error">没有符合条件的资源。</p>
    <div class="resource-grid">
      <article v-for="m in items" :key="`${m.media_id}/${m.variant_id??''}`">
        <h3>{{m.title||'未命名发布'}} · {{mediaRoleLabel(m.role)}} {{m.position}}</h3>
        <p>{{m.creator_name??'未关联创作者'}} · {{m.namespace}} / {{m.account_name??m.account_key??'来源账号未知'}}</p>
        <p>{{m.available?`可用副本 ${m.verified_copies}`:'未保存或副本缺失'}} · {{formatBytes(m.size)}} · {{spec(m.measured_spec)}}</p>
        <p>{{authLabel(m.authentication?.state)}} · 保存时间 {{formatTime(m.saved_at)}} {{m.selected?'· 当前选择':''}}</p>
        <button :disabled="!m.available" @click="selected=m">查看资源</button>
        <router-link :to="{path:'/',query:{entry:m.entry_id}}">回到发布记录与来源</router-link>
      </article>
    </div>
    <button v-if="next" :disabled="busy" @click="load(true)">{{busy?'读取中…':'加载更多资源'}}</button>
    <section v-if="selected" aria-label="资源预览"><h3>{{selected.title}}</h3><button @click="selected=undefined">关闭预览</button><MediaBrowser :media="[selected]"/></section>
  </section>
</template>
<style scoped>
.resource-library{max-width:1200px;margin:auto}.filters{display:flex;flex-wrap:wrap;gap:12px;margin:16px 0}label{display:grid;gap:5px}select,button{border:1px solid #cddbeb;border-radius:6px;padding:8px;background:white;color:#245fc8}.resource-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,320px),1fr));gap:12px}article{border:1px solid #dce4ee;background:white;padding:16px;border-radius:8px;overflow-wrap:anywhere}h3{font-size:15px}p{font-size:13px;color:#586981}a{margin-left:12px;color:#245fc8}
</style>
