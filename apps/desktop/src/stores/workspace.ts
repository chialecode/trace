import { defineStore } from "pinia";
import { isTauri } from "@tauri-apps/api/core";
export const useWorkspaceStore = defineStore("workspace", {
  state: () => ({ query: "", platform: "", preview: !isTauri() }),
});
