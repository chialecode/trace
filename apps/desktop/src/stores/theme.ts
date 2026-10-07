import { defineStore } from "pinia";

// 语义主题入口：默认跟随系统，可手动选择；视觉 Token 数值由 Q-003 集中确认。
export const useThemeStore = defineStore("theme", {
  state: () => ({
    isDark: window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false,
    manual: false as boolean,
  }),
  actions: {
    setManual(isDark: boolean) {
      this.isDark = isDark;
      this.manual = true;
    },
    followSystem() {
      this.manual = false;
      this.isDark = window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
    },
  },
});
