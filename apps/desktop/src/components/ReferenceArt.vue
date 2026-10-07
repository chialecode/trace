<script setup lang="ts">
import { ref, watch } from 'vue';
const props = defineProps<{ name: string; alt: string; local?: boolean }>();
const failed = ref(false);
watch(() => props.name, () => { failed.value = false; });
const isDev = import.meta.env.DEV;
</script>
<template>
  <span class="reference-art" :class="`art-${name}`">
    <img v-if="isDev && local && !failed" :src="`/__reference-assets/${name}.webp`" :alt="alt" @error="failed = true" />
    <span v-else class="art-fallback" role="img" :aria-label="alt || '暂无封面'">{{ name === 'portrait' ? '森' : '' }}</span>
  </span>
</template>
<style scoped>
.reference-art{display:block;overflow:hidden;background:#e6effc;width:100%;height:100%}.reference-art img{width:100%;height:100%;object-fit:cover;display:block}.art-fallback{display:grid;place-items:center;width:100%;height:100%;font-size:28px;color:#5682c8;background:radial-gradient(ellipse at 25% 15%,#fff 0,transparent 35%),linear-gradient(160deg,#d7eaff 35%,#a4c6ed 36%,#f3f8ff 55%,#7aa9d7 56%,#cadffc 85%)}.art-city .art-fallback{background:linear-gradient(170deg,#bad4f5,#ffdfd8 55%,#9caacd 56%,#6c8fb6)}
</style>
