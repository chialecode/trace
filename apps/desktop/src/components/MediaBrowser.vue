<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { mediaRoleLabel } from '../lib/format';
import { api, mediaUrl, type MediaDto } from '../lib/api';
const props = defineProps<{ media: MediaDto[] }>();
const current = ref('');
const feedback = ref('');
const opening = ref(false);
const selected = computed(() => props.media.find(m => m.variant_id === current.value));
watch(() => props.media, list => {
  if (list.some(m => m.variant_id === current.value && m.available && m.blob_id)) return;
  const lost = Boolean(current.value);
  current.value = (list.find(m => m.available && m.representation === 'playback-mux-v1') ?? list.find(m => m.available && m.role !== 'cover'))?.variant_id ?? '';
  feedback.value = lost ? '原媒体资源已不可用，请选择可用资源。' : '';
}, { immediate: true });
async function openExternal() {
  const blob = selected.value?.blob_id;
  if (!blob) return;
  opening.value = true; feedback.value = '';
  try { await api.openExternal(blob); feedback.value = '已交给系统媒体程序打开。'; }
  catch { feedback.value = '无法打开：文件可能缺失、类型不支持或后台未连接。请刷新档案后重试。'; }
  finally { opening.value = false; }
}
</script>
<template>
  <section class="media-browser" aria-label="本地媒体">
    <p v-if="!media.length">尚未发现媒体资源。</p>
    <label v-else>媒体资源
      <select v-model="current" aria-label="选择媒体资源" @change="feedback = ''">
        <option value="">选择资源</option>
        <option v-for="(m,index) in media" :key="m.variant_id ?? m.media_id" :value="m.variant_id ?? ''" :disabled="!m.available || !m.blob_id">{{ mediaRoleLabel(m.role) }} · {{ m.position }} {{ m.available ? '' : '· 文件未保存或缺失' }} {{ index + 1 }}</option>
      </select>
    </label>
    <template v-if="selected?.available && mediaUrl(selected.blob_id)">
      <video v-if="selected.mime?.startsWith('video/')" :key="current" :src="mediaUrl(selected.blob_id)" controls preload="metadata" @error="feedback = '内置播放器无法播放此编码或文件已不可用。可尝试外部打开。'"/>
      <audio v-else-if="selected.mime?.startsWith('audio/')" :key="current" :src="mediaUrl(selected.blob_id)" controls preload="metadata" @error="feedback = '音频无法播放，请尝试外部打开。'"/>
      <img v-else-if="selected.mime?.startsWith('image/')" :src="mediaUrl(selected.blob_id)" alt="已保存图片" @error="feedback = '图片不可用，请刷新档案。'"/>
      <p v-else>此媒体类型无法在内置播放器中预览。</p>
      <button class="primary-button" :disabled="opening" @click="openExternal">{{ opening ? '正在校验文件…' : '外部打开已保存媒体' }}</button>
    </template>
    <p v-if="media.some(m => !m.available)" class="media-note">部分资源未保存或副本缺失；已保存的资源仍可浏览。</p>
    <p v-if="media.some(m => m.role === 'audio')" class="media-note">分轨来源保留独立音频资源，可在上方选择。</p>
    <p v-if="feedback" role="status">{{ feedback }}</p>
  </section>
</template>
<style scoped>
.media-browser { display: grid; gap: 12px; margin-top: 16px; }
label { display: grid; gap: 8px; }
select { max-width: 100%; padding: 8px; border: 1px solid #dce4ee; border-radius: 6px; }
video, audio, img { width: 100%; max-height: 320px; object-fit: contain; }
p { font-size: 12px; color: #60708a; overflow-wrap: anywhere; }
</style>
