import { defineStore } from "pinia";

export const TEXT_SCALES = [100, 110, 125, 150] as const;
export type TextScale = (typeof TEXT_SCALES)[number];
export const TEXT_SCALE_KEY = "trace.display.text-scale";

const CHANNEL = "trace-display-preferences";
let channel: BroadcastChannel | null = null;

export function validTextScale(value: unknown): TextScale | null {
  const number = typeof value === "number" ? value : Number(value);
  return TEXT_SCALES.includes(number as TextScale) ? (number as TextScale) : null;
}

export function scaledPx(pixels: number, scale: number) {
  return `${(pixels * scale) / 100}px`;
}

function apply(scale: TextScale) {
  const root = document.documentElement;
  root.dataset.textScale = String(scale);
  root.style.setProperty("--trace-root-font-size", scaledPx(16, scale));
}

export const useDisplayStore = defineStore("display", {
  state: () => ({
    scale: 100 as TextScale,
    warning: "",
    initialized: false,
  }),
  actions: {
    init() {
      if (this.initialized) return;
      this.initialized = true;
      let stored: string | null = null;
      try {
        stored = window.localStorage.getItem(TEXT_SCALE_KEY);
      } catch {
        this.warning = "无法读取本机显示设置，已使用 100% 文字大小。";
      }
      const parsed = stored === null ? (100 as TextScale) : validTextScale(stored);
      if (stored !== null && parsed === null) {
        this.warning = "保存的文字大小无效，已恢复 100%。";
      }
      this.scale = parsed ?? 100;
      apply(this.scale);
      window.addEventListener("storage", (event) => this.receive(event));
      try {
        channel = new BroadcastChannel(CHANNEL);
        channel.addEventListener("message", (event) => this.receiveValue(event.data));
      } catch {
        channel = null;
      }
    },
    receive(event: StorageEvent) {
      if (event.key === TEXT_SCALE_KEY) this.receiveValue(event.newValue);
    },
    receiveValue(value: unknown) {
      const parsed = validTextScale(value);
      if (parsed === null || parsed === this.scale) return;
      this.scale = parsed;
      apply(parsed);
      this.warning = "";
    },
    setScale(value: unknown) {
      const parsed = validTextScale(value);
      if (parsed === null) {
        this.warning = "只支持 100%、110%、125% 和 150% 四档文字大小。";
        return;
      }
      this.scale = parsed;
      apply(parsed);
      try {
        window.localStorage.setItem(TEXT_SCALE_KEY, String(parsed));
        this.warning = "";
      } catch {
        this.warning = "文字大小已即时应用，但本机存储不可用，重开后可能恢复默认值。";
      }
      channel?.postMessage(parsed);
    },
  },
});
