<script setup lang="ts">
import { ref,watch,onBeforeUnmount } from 'vue';
import { api,type QualityVariantDto } from '../lib/api';
const props=defineProps<{entryId:string}>();
const preference=ref('resolution'),status=ref('unknown'),next=ref<number>();
const items=ref<QualityVariantDto[]>([]);const busy=ref(false);const error=ref('');const opened=ref(false);const wait=ref(86400);let generation=0;
const reasons:Record<string,string>={selected:'当前选中',replaced_by_verified_quality:'已由验证通过的更高质量替换',unique_live_coverage:'保留独有录制区间',selection_pinned:'已固定当前选择',derived_copy_preserves_sources:'保留派生文件的原件',incomparable_duration_or_tracks:'时长或音轨不同，保留待比较',unknown_measured_quality:'实测规格不足，继续保留',comparable_same_attachment:'同一内容资源，可比较规格',manual_keep:'手动保留'};
async function load(older=false){
  const ticket=++generation;busy.value=true;error.value='';
  try{
    let cursor=older?next.value:undefined;const target=older?0:items.value.length;const fetched:QualityVariantDto[]=[];
    do{const result=await api.qualityList(props.entryId,cursor);if(ticket!==generation)return;fetched.push(...result.items);cursor=result.next_after;wait.value=result.recycle_wait_seconds;preference.value=result.preference;status.value=result.state??'unknown';}while(!older&&cursor!==undefined&&cursor!==null&&fetched.length<target);
    const merged=new Map((older?items.value:[]).map(v=>[v.variant_id,v]));for(const v of fetched)merged.set(v.variant_id,v);items.value=[...merged.values()];next.value=cursor;
  }catch{if(ticket===generation)error.value='质量与保留信息读取失败，可重新展开重试。';}finally{if(ticket===generation)busy.value=false;}
}
async function configure(){const entry=props.entryId;busy.value=true;error.value='';try{await api.qualityConfigure(entry,preference.value==='frame_rate',wait.value);if(entry===props.entryId)await load();}catch{if(entry===props.entryId)error.value='质量偏好未能保存，请重试。';}finally{if(entry===props.entryId)busy.value=false;}}
async function pin(item:QualityVariantDto){const entry=props.entryId;busy.value=true;error.value='';try{await api.qualityPin(item.variant_id,!item.pinned);if(props.entryId===entry)await load();}catch{if(props.entryId===entry)error.value='保留状态未能更新，原文件仍受已有规则保护。';}finally{if(props.entryId===entry)busy.value=false;}}
function toggle(event:Event){const was=opened.value;opened.value=(event.currentTarget as HTMLDetailsElement).open;if(opened.value&&!was)void load();}
watch(()=>props.entryId,()=>{generation++;items.value=[];next.value=undefined;error.value='';busy.value=false;if(opened.value)void load();});onBeforeUnmount(()=>generation++);
</script>
<template><details class="quality-panel" @toggle="toggle"><summary>质量与保留</summary>
  <p>更高质量验证通过、必要音轨完整后才切换。旧副本至少等待 {{ Math.round(wait/3600*10)/10 }} 小时，共享文件和独有录制区间继续保留。</p>
  <label>默认质量偏好 <select v-model="preference" :disabled="busy"><option value="resolution">优先分辨率</option><option value="frame_rate">优先帧率</option></select></label><label>回收等待（秒）<input v-model.number="wait" type="number" min="0" max="2592000" :disabled="busy"/></label><button :disabled="busy" @click="configure">保存偏好</button>
  <p>当前质量：{{status==='satisfied'?'已验证达到已观察目标':status==='degraded'?'与质量目标存在差距或来源能力下降':'尚无充分比较证据'}}。默认偏好适用于后续解析；现有变体立即重新比较。</p>
  <p v-if="busy" role="status">正在读取或更新…</p><p v-if="error" role="alert">{{error}}</p>
  <ul><li v-for="item in items" :key="item.variant_id"><span>{{ item.selected?'当前选中 · ':'' }}{{ item.measured?.width && item.measured?.height ? `${item.measured.width}×${item.measured.height}` : '规格未知' }}{{ item.measured?.fps ? ` · ${item.measured.fps} 帧/秒` : '' }}</span><small>{{reasons[item.reason??'']??'尚未建立可替换证据，继续保留'}}</small><button :disabled="busy" @click="pin(item)">{{item.pinned?'取消手动保留':'手动保留'}}</button></li></ul>
  <button v-if="next" :disabled="busy" @click="load(true)">更多质量版本</button>
  <p v-if="!busy&&!error&&!items.length">尚无已保存的媒体版本。</p>
</details></template>
<style scoped>.quality-panel{margin:16px 0;font-size:12px}summary{cursor:pointer;font-weight:600}p,small{color:#60728a}ul{padding:0;list-style:none}li{padding:10px 0;border-bottom:1px solid #e4eaf3;display:flex;gap:6px;flex-wrap:wrap}small{flex-basis:100%}button{border:1px solid #c8d7eb;color:#245fc8;background:#eef4ff;border-radius:5px;padding:4px 8px}</style>
