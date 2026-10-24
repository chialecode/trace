import { flushPromises, mount, shallowMount } from "@vue/test-utils";
import { createPinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import { describe, expect, it, vi } from "vitest";
import { isTauri, invoke } from "@tauri-apps/api/core";
import WindowTitlebar from "../WindowTitlebar.vue";
import App from "../../App.vue";
import { api } from "../../lib/api";

vi.mock("@tauri-apps/api/core", () => ({
  isTauri: vi.fn(() => false),
  invoke: vi.fn(),
}));
vi.mock("../../lib/api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../lib/api")>();
  return {
    ...original,
    api: { ...original.api, pluginList: vi.fn().mockResolvedValue({ items: [] }) },
  };
});

describe("immersive window titlebar", () => {
  it("uses real Tauri window actions and updates the restore state", async () => {
    vi.mocked(invoke).mockImplementation(async (command: string) => {
      if (command === "desktop_window_state") return { maximized: false };
      if (command === "desktop_window_toggle_maximize") return { maximized: true };
      if (command === "desktop_window_minimize") return { minimized: true };
      if (command === "desktop_close") return { closed: true };
      throw new Error("unexpected command");
    });
    const wrapper = mount(WindowTitlebar);
    await flushPromises();
    expect(wrapper.get(".window-drag-region").attributes("data-tauri-drag-region")).toBeDefined();
    expect(wrapper.get(".window-drag-space").attributes("data-tauri-drag-region")).toBeDefined();
    expect(wrapper.get('[data-action="window-toggle"]').attributes("aria-label")).toBe(
      "最大化窗口",
    );
    await wrapper.get('[data-action="window-toggle"]').trigger("click");
    await flushPromises();
    expect(invoke).toHaveBeenCalledWith("desktop_window_toggle_maximize");
    expect(wrapper.get('[data-action="window-toggle"]').attributes("aria-label")).toBe("还原窗口");
    await wrapper.get('[aria-label="最小化窗口"]').trigger("click");
    await flushPromises();
    expect(invoke).toHaveBeenCalledWith("desktop_window_minimize");
    await wrapper.get('[data-action="window-close"]').trigger("click");
    await flushPromises();
    expect(invoke).toHaveBeenCalledWith("desktop_close");
  });

  it("shows a visible error instead of silently pretending a window action succeeded", async () => {
    vi.mocked(invoke).mockRejectedValue(new Error("native failure"));
    const wrapper = mount(WindowTitlebar);
    await flushPromises();
    await wrapper.get('[data-action="window-toggle"]').trigger("click");
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain("无法最大化或还原窗口");
  });

  it("does not show fake native controls in the browser example", async () => {
    vi.mocked(isTauri).mockReturnValue(false);
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/", component: { template: "<div>home</div>" } }],
    });
    await router.push("/");
    await router.isReady();
    const wrapper = shallowMount(App, {
      global: {
        plugins: [createPinia(), router],
        stubs: {
          NConfigProvider: { template: "<div><slot /></div>" },
          NMessageProvider: { template: "<div><slot /></div>" },
          NDialogProvider: { template: "<div><slot /></div>" },
        },
      },
    });
    await flushPromises();
    expect(wrapper.find(".window-titlebar").exists()).toBe(false);
    expect(api.pluginList).not.toHaveBeenCalled();
  });
});
