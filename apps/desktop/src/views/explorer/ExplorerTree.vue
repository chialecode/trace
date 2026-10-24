<script setup lang="ts">
import { ref } from "vue";
import type { ExplorerNode } from "../../lib/api";
import { displayText } from "../../lib/safe";
import type { VisibleNode } from "./useExplorer";

const props = defineProps<{
  items: VisibleNode[];
  selected: string | null;
  loading: boolean;
  error: string;
  truncated: Record<string, boolean>;
}>();
const emit = defineEmits<{
  select: [node: ExplorerNode];
  toggle: [node: ExplorerNode];
  retry: [];
}>();

const root = ref<HTMLElement | null>(null);
const focused = ref(0);

function focusAt(index: number) {
  const clamped = Math.max(0, Math.min(props.items.length - 1, index));
  focused.value = clamped;
  const rows = root.value?.querySelectorAll<HTMLElement>('[role="treeitem"]');
  rows?.[clamped]?.focus();
}
function parentIndex(index: number) {
  const depth = props.items[index]?.depth ?? 0;
  for (let i = index - 1; i >= 0; i--) if ((props.items[i]?.depth ?? 0) < depth) return i;
  return index;
}
function onKey(event: KeyboardEvent, index: number) {
  const item = props.items[index];
  if (!item) return;
  const keys: Record<string, () => void> = {
    ArrowDown: () => focusAt(index + 1),
    ArrowUp: () => focusAt(index - 1),
    Home: () => focusAt(0),
    End: () => focusAt(props.items.length - 1),
    ArrowRight: () => {
      if (item.node.expandable && !item.expanded) emit("toggle", item.node);
      else focusAt(index + 1);
    },
    ArrowLeft: () => {
      if (item.expanded) emit("toggle", item.node);
      else focusAt(parentIndex(index));
    },
    Enter: () => emit("select", item.node),
    " ": () => emit("select", item.node),
  };
  const run = keys[event.key];
  if (!run) return;
  event.preventDefault();
  run();
}
</script>

<template>
  <nav class="explorer-tree" aria-label="文件夹">
    <p v-if="loading && !items.length" class="page-note" role="status">正在读取文件夹…</p>
    <div v-else-if="error" class="state-box" role="alert">
      <span>无法读取文件夹：{{ error }}</span>
      <button class="tool-button" @click="emit('retry')">重试</button>
    </div>
    <ul v-else ref="root" role="tree" aria-label="资源文件夹">
      <li
        v-for="(item, index) in items"
        :key="item.node.node"
        role="treeitem"
        :aria-level="item.depth + 1"
        :aria-expanded="item.node.expandable ? item.expanded : undefined"
        :aria-selected="selected === item.node.node"
        :tabindex="focused === index ? 0 : -1"
        :class="{ current: selected === item.node.node }"
        :style="{ paddingInlineStart: `${item.depth * 14 + 4}px` }"
        @keydown="onKey($event, index)"
        @focus="focused = index"
      >
        <button
          v-if="item.node.expandable"
          class="twisty"
          tabindex="-1"
          :aria-label="(item.expanded ? '收起 ' : '展开 ') + displayText(item.node.name)"
          @click.stop="emit('toggle', item.node)"
        >
          {{ item.loading ? "…" : item.expanded ? "▾" : "▸" }}
        </button>
        <span v-else class="twisty" aria-hidden="true"></span>
        <span class="label" @click="emit('select', item.node)">
          <bdi class="untrusted-text">{{ displayText(item.node.name) }}</bdi>
          <small v-if="item.node.count !== null" class="count">{{ item.node.count }}</small>
        </span>
        <small v-if="truncated[item.node.node]" class="page-note">文件夹太多，只列出了一部分</small>
      </li>
    </ul>
  </nav>
</template>

<style scoped>
.explorer-tree {
  min-width: 0;
}
ul {
  margin: 0;
  padding: 0;
  list-style: none;
}
li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 2px;
  min-height: 28px;
  border-radius: 6px;
  cursor: default;
}
li.current {
  background: #e8f0ff;
  font-weight: 600;
}
li:hover {
  background: #f1f6ff;
}
.twisty {
  width: 20px;
  height: 20px;
  padding: 0;
  border: 0;
  background: none;
  color: #51627b;
  cursor: pointer;
}
.label {
  display: inline-flex;
  flex: 1;
  min-width: 0;
  gap: 6px;
  align-items: baseline;
  cursor: pointer;
}
.count {
  color: #7a889e;
}
</style>
