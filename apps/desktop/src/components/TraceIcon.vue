<script setup lang="ts">
withDefaults(defineProps<{ name: string; size?: number }>(), { size: 20 });
const paths: Record<string, string> = {
  home: 'M3 10 12 3l9 7M5 9v12h5v-7h4v7h5V9',
  users: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M17 4a4 4 0 0 1 0 8M22 21v-2a4 4 0 0 0-3-3.87M13 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0',
  file: 'M14 2H5v20h14V7l-5-5v5h5M8 12h8M8 16h8',
  download: 'M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5',
  video: 'M3 6h12v12H3zM15 10l6-3v10l-6-3',
  play: 'm8 5 11 7-11 7z',
  search: 'M20 20l-5-5M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0',
  image: 'M3 3h18v18H3zM3 16l6-6 5 5 3-3 4 4M16 7h.01',
  bell: 'M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4',
  heart: 'M20.8 4.6a5.5 5.5 0 0 0-7.8 0L12 5.7l-1.1-1.1a5.5 5.5 0 0 0-7.8 7.8L12 21l8.8-8.6a5.5 5.5 0 0 0 0-7.8',
  chat: 'M21 4H3v14h5v4l5-4h8z',
  external: 'M14 3h7v7m0-7L10 14M10 3H3v18h18v-7',
  chevron: 'm9 5 7 7-7 7', down: 'm6 9 6 6 6-6', plus: 'M12 4v16M4 12h16',
  check: 'm5 12 4 4L19 6', collection: 'm3 8 9-6 9 6v13H3zM9 11h6v6H9z',
  activity: 'm8 6-6 6 6 6m8-12 6 6-6 6M13 3l-2 18',
  settings: 'M9 3h6l1 3 3 1 2 5-2 5-3 1-1 3H9l-1-3-3-1-2-5 2-5 3-1zM15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0',
  refresh: 'M20 7V2m0 5h-5M4 17v5m0-5h5M20 7A9 9 0 0 0 4 6M4 17a9 9 0 0 0 16 1',
  close: 'm6 6 12 12M6 18 18 6',
};
</script>
<template>
  <svg :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path v-if="paths[name]" :d="paths[name]" />
    <g v-else><circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/></g>
  </svg>
</template>
