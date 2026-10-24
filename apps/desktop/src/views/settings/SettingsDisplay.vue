<script setup lang="ts">
import { TEXT_SCALES, useDisplayStore } from "../../stores/display";

const display = useDisplayStore();
display.init();
</script>

<template>
  <section id="set-display" class="settings-card display-settings" aria-labelledby="display-title">
    <header>
      <div>
        <h3 id="display-title">显示与文字</h3>
        <p class="page-note">
          只影响这台电脑上的界面显示，不改变档案、下载质量或网站内容。布局会随文字大小重新排列。
        </p>
      </div>
      <span class="current-scale" aria-live="polite">{{ display.scale }}%</span>
    </header>
    <div class="scale-choices" role="radiogroup" aria-label="文字大小">
      <button
        v-for="scale in TEXT_SCALES"
        :key="scale"
        type="button"
        class="tool-button"
        :class="{ selected: display.scale === scale }"
        role="radio"
        :aria-checked="display.scale === scale"
        :data-text-scale-option="scale"
        @click="display.setScale(scale)"
      >
        {{ scale }}%
      </button>
    </div>
    <p v-if="display.warning" class="scale-warning" role="alert">{{ display.warning }}</p>
  </section>
</template>

<style scoped>
.settings-card {
  display: grid;
  gap: 0.75rem;
  padding: 1rem;
  border: 1px solid #e6eefb;
  border-radius: 0.5625rem;
  background: #fff;
}
header {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.75rem;
}
h3 {
  margin: 0 0 0.25rem;
}
.current-scale {
  padding: 0.125rem 0.625rem;
  border-radius: 999px;
  background: #e8f1ff;
  color: #245db8;
  font-weight: 600;
}
.scale-choices {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
.selected {
  border-color: #3979ff;
  background: #3979ff;
  color: #fff;
}
.scale-warning {
  margin: 0;
  color: #8a2a21;
}
</style>
