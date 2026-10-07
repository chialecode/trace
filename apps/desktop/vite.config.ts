import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { referenceAssets } from "./reference-assets.ts";

// clearScreen: false —— Tauri 开发时保留 CLI 输出；固定端口与前端 devUrl 一致。
export default defineConfig({
  clearScreen: false,
  plugins: [vue(), referenceAssets()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
  },
  build: {
    target: "chrome110",
    sourcemap: false,
    chunkSizeWarningLimit: 700,
    // vite 8（rolldown）要求 manualChunks 为函数形式。
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes("naive-ui")) return "ui";
          if (id.includes("node_modules")) return "vendor";
          return undefined;
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.spec.ts", "src/**/*.test.ts"],
  },
});
