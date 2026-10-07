<script setup lang="ts">
import { computed } from 'vue';
import { mediaUrl, type MediaDto, type DocumentDto } from '../lib/api';
const props = defineProps<{ document?: DocumentDto; media: MediaDto[]; body: string }>();
const local = computed(() => {const result=new Map<string|undefined,MediaDto>();for(const m of props.media.filter(m=>m.available&&m.blob_id&&!['pending','rejected'].includes(m.approval_state??'automatic'))){if(!result.has(m.attachment_id)||m.selected)result.set(m.attachment_id,m);}return result;});
const heading=(style:string)=>/^h[1-6]$/.test(style)?style:'p';
</script>
<template>
  <article class="archive-document" aria-label="离线正文">
    <template v-if="document?.blocks?.length">
      <template v-for="(block,index) in document.blocks" :key="index">
        <component v-if="block.kind==='rich_text'" :is="block.style==='quote'?'blockquote':block.style==='ordered'?'ol':block.style==='bullet'?'ul':heading(block.style)" :start="block.ordinal ?? undefined" :style="block.style==='ordered'||block.style==='bullet'?{marginLeft:`${Math.min(8,Math.max(1,block.depth))*12}px`}:undefined">
          <component :is="block.style==='ordered'||block.style==='bullet'?'li':'span'"><span v-for="(span,n) in block.spans" :key="n" :class="{'inline-bold':span.bold,'inline-italic':span.italic,'inline-strike':span.strike}">{{span.text}}</span></component>
        </component>
        <figure v-else-if="block.kind === 'resource'">
          <img v-if="local.get(block.attachment_id)?.mime?.startsWith('image/')" :src="mediaUrl(local.get(block.attachment_id)?.blob_id)" :alt="block.caption || '正文配图'" loading="lazy"/>
          <p v-else class="missing">{{ block.caption || '正文资源' }} · 未保存或暂不可预览</p>
          <figcaption v-if="block.caption">{{ block.caption }}</figcaption>
        </figure>
        <pre v-else-if="block.kind === 'text' && block.style === 'code'">{{ block.text }}</pre>
        <div v-else-if="block.kind==='table'" class="table-scroll"><table><tbody><tr v-for="(row,rowIndex) in block.rows" :key="rowIndex"><component :is="block.header && rowIndex===0 ? 'th' : 'td'" v-for="(cell,cellIndex) in row" :key="cellIndex">{{cell}}</component></tr></tbody></table></div>
        <ol v-else-if="block.kind==='list_item' && block.ordinal!=null" :start="block.ordinal" :style="{marginLeft:`${Math.min(8,Math.max(1,block.depth))*12}px`}"><li>{{block.text}}</li></ol>
        <ul v-else-if="block.kind==='list_item'" :style="{marginLeft:`${Math.min(8,Math.max(1,block.depth))*12}px`}"><li>{{block.text}}</li></ul>
        <blockquote v-else-if="block.kind === 'text' && block.style === 'quote'">{{ block.text }}</blockquote>
        <component :is="heading(block.style)" v-else-if="block.kind === 'text' && /^h[1-6]$/.test(block.style)">{{block.text}}</component>
        <p v-else>{{ block.kind === 'text' || block.kind === 'link' ? block.text : '' }}</p>
      </template>
      <p v-if="document.omissions?.length" role="status">部分正文结构或资源尚未取得，完整性待补全。</p>
    </template>
    <p v-else>{{ body || '暂无正文' }}</p>
  </article>
</template>
<style scoped>
.archive-document { overflow-wrap: anywhere; white-space: pre-wrap; line-height: 1.75; }
.inline-bold{font-weight:700}.inline-italic{font-style:italic}.inline-strike{text-decoration:line-through}
figure { margin: 12px 0; } img { max-width: 100%; height: auto; } pre { overflow: auto; white-space: pre-wrap; }
.table-scroll{max-width:100%;overflow:auto}table{border-collapse:collapse}td,th{border:1px solid #dce4ee;padding:5px 10px;text-align:left}ul,ol{padding-left:18px;margin-top:2px;margin-bottom:2px}
.missing,figcaption { color: #60708a; font-size: 12px; } blockquote { border-left: 3px solid #3979ff; margin: 12px 0; padding-left: 12px; }
</style>
