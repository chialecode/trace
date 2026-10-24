<script setup lang="ts">
import { invoke } from "@tauri-apps/api/core";
import { onBeforeUnmount, onMounted, ref } from "vue";

const maximized = ref(false);
const error = ref("");
let alive = true;

async function state() {
  try {
    const result = await invoke<{ maximized: boolean }>("desktop_window_state");
    if (alive) {
      maximized.value = result.maximized;
      error.value = "";
    }
  } catch {
    if (alive) error.value = "无法读取窗口状态";
  }
}

async function minimize() {
  try {
    await invoke("desktop_window_minimize");
    error.value = "";
  } catch {
    error.value = "无法最小化窗口";
  }
}

async function toggle() {
  try {
    const result = await invoke<{ maximized: boolean }>("desktop_window_toggle_maximize");
    maximized.value = result.maximized;
    error.value = "";
  } catch {
    error.value = "无法最大化或还原窗口";
  }
}

async function close() {
  try {
    await invoke("desktop_close");
    error.value = "";
  } catch {
    error.value = "无法关闭窗口";
  }
}

function resize() {
  void state();
}

onMounted(() => {
  void state();
  window.addEventListener("resize", resize);
});
onBeforeUnmount(() => {
  alive = false;
  window.removeEventListener("resize", resize);
});
</script>

<template>
  <header class="window-titlebar">
    <div class="window-drag-region" data-tauri-drag-region>
      <span class="titlebar-mark" aria-hidden="true">T</span>
      <span>TRACE 档案</span>
    </div>
    <div class="window-drag-space" data-tauri-drag-region></div>
    <span v-if="error" class="window-error" role="alert">{{ error }}</span>
    <div class="window-controls" aria-label="窗口控制">
      <button type="button" aria-label="最小化窗口" title="最小化" @click="minimize">
        <svg viewBox="0 0 12 12" aria-hidden="true"><path d="M2 8.5h8" /></svg>
      </button>
      <button
        type="button"
        :aria-label="maximized ? '还原窗口' : '最大化窗口'"
        :title="maximized ? '还原' : '最大化'"
        data-action="window-toggle"
        @click="toggle"
      >
        <svg v-if="!maximized" viewBox="0 0 12 12" aria-hidden="true">
          <rect x="2.2" y="2.2" width="7.6" height="7.6" />
        </svg>
        <svg v-else viewBox="0 0 12 12" aria-hidden="true">
          <path d="M4 3V1.8h6.2V8H9M2 4h6v6H2z" />
        </svg>
      </button>
      <button
        type="button"
        class="window-close"
        aria-label="关闭窗口，后台继续运行"
        title="关闭窗口（后台继续运行）"
        data-action="window-close"
        @click="close"
      >
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <path d="m2.2 2.2 7.6 7.6m0-7.6L2.2 9.8" />
        </svg>
      </button>
    </div>
  </header>
</template>

<style scoped>
.window-titlebar {
  height: 2.5rem;
  flex: 0 0 2.5rem;
  display: flex;
  align-items: stretch;
  border-bottom: 1px solid #e4edfa;
  background: linear-gradient(120deg, #f4f9ff, #fbfdff 58%, #f2f7ff);
  user-select: none;
}
.window-drag-region,
.window-drag-space {
  -webkit-app-region: drag;
}
.window-drag-region {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0 0.875rem;
  color: #4b6387;
  font-size: 0.75rem;
  letter-spacing: 0.04em;
}
.titlebar-mark {
  display: inline-grid;
  place-items: center;
  width: 1.25rem;
  height: 1.25rem;
  border-radius: 0.375rem;
  background: #3979ff;
  color: #fff;
  font-size: 0.75rem;
  font-weight: 700;
}
.window-drag-space {
  flex: 1;
  min-width: 2rem;
}
.window-error {
  align-self: center;
  max-width: 18rem;
  overflow: hidden;
  color: #9b2c22;
  font-size: 0.75rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.window-controls {
  display: flex;
  height: 100%;
}
.window-controls button {
  width: 2.875rem;
  height: 100%;
  display: grid;
  place-items: center;
  padding: 0;
  border: 0;
  background: transparent;
  color: #415671;
}
.window-controls button:hover {
  background: #e7f0ff;
}
.window-controls .window-close:hover {
  background: #d93a32;
  color: #fff;
}
.window-controls svg {
  width: 0.75rem;
  height: 0.75rem;
  fill: none;
  stroke: currentColor;
  stroke-width: 1;
}
</style>
