<script setup lang="ts">
import { computed } from "vue";
import type { ProfileItem } from "../../lib/api";
import { formatTime, saveProgressLabel } from "../../lib/format";
import { displayText } from "../../lib/safe";
import { contentSummary } from "../../lib/sites";

const props = defineProps<{ items: ProfileItem[]; selected: Set<string> }>();
const emit = defineEmits<{
  toggle: [key: string, on: boolean];
  range: [keys: string[], on: boolean];
}>();

let anchor: number | null = null;
const keys = computed(() => props.items.map((i) => i.native_key));

/** Shift-click selects everything between the last clicked row and this one. */
function change(index: number, event: Event) {
  const input = event.target as HTMLInputElement;
  const key = keys.value[index];
  if (key === undefined) return;
  if ((event as MouseEvent).shiftKey && anchor !== null) {
    const [a, b] = [Math.min(anchor, index), Math.max(anchor, index)];
    emit("range", keys.value.slice(a, b + 1), input.checked);
  } else emit("toggle", key, input.checked);
  anchor = index;
}
function summary(item: ProfileItem) {
  return contentSummary({
    content_kinds: item.content_kinds,
    image_count: item.image_count,
    video_count: item.video_count,
    duration_ms: item.duration_ms,
  });
}
</script>

<template>
  <ul class="profile-items" aria-label="主页内容">
    <li v-for="(item, index) in items" :key="item.native_key" :class="{ held: item.in_library }">
      <label class="pick">
        <input
          type="checkbox"
          :checked="selected.has(item.native_key)"
          :aria-label="'选择 ' + (displayText(item.title) || displayText(item.native_key))"
          @click="change(index, $event)"
        />
      </label>
      <div class="body">
        <strong class="title untrusted-text">{{
          displayText(item.title) || displayText(item.native_key)
        }}</strong>
        <span v-if="item.pinned" class="badge">置顶</span>
        <span v-if="item.in_library" class="badge success"
          >已在资源库 · {{ saveProgressLabel(item.save_progress) }}</span
        >
        <p v-if="item.summary" class="summary">{{ displayText(item.summary) }}</p>
        <p class="page-note">
          {{ item.published_at ? formatTime(item.published_at) : "发布时间未知" }}
          <template v-if="summary(item)"> · {{ summary(item) }}</template>
        </p>
      </div>
    </li>
  </ul>
</template>

<style scoped>
.profile-items {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}
li {
  display: flex;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid #e3ebf7;
  border-radius: 8px;
  background: #fff;
}
li.held {
  background: #f7faf8;
}
.pick {
  display: flex;
  align-items: flex-start;
  padding-top: 2px;
}
.body {
  display: grid;
  gap: 2px;
  min-width: 0;
  flex: 1;
}
.body p {
  margin: 0;
}
.title {
  max-width: 100%;
  white-space: normal;
  overflow-wrap: anywhere;
}
.summary {
  color: #51627b;
  font-size: 0.8125rem;
  overflow-wrap: anywhere;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
</style>
