import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import SettingsDisplay from "../settings/SettingsDisplay.vue";
import { TEXT_SCALE_KEY, scaledPx, useDisplayStore, validTextScale } from "../../stores/display";

describe("display and text scale", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-text-scale");
    document.documentElement.style.removeProperty("--trace-root-font-size");
    setActivePinia(createPinia());
  });
  afterEach(() => vi.restoreAllMocks());

  it("applies all four real root sizes immediately and persists the choice", async () => {
    const wrapper = mount(SettingsDisplay);
    expect(document.documentElement.dataset.textScale).toBe("100");
    expect(document.documentElement.style.fontSize).toBe("");
    expect(
      getComputedStyle(document.documentElement).getPropertyValue("--trace-root-font-size").trim(),
    ).toBe("16px");
    for (const scale of [110, 125, 150, 100]) {
      await wrapper.get(`[data-text-scale-option="${scale}"]`).trigger("click");
      expect(document.documentElement.dataset.textScale).toBe(String(scale));
      expect(localStorage.getItem(TEXT_SCALE_KEY)).toBe(String(scale));
      expect(document.documentElement.style.getPropertyValue("--trace-root-font-size")).toBe(
        scaledPx(16, scale),
      );
    }
    expect(wrapper.get('[aria-label="文字大小"]').attributes("role")).toBe("radiogroup");
    expect(wrapper.get('[data-text-scale-option="100"]').attributes("aria-checked")).toBe("true");
  });

  it("restores a saved value and follows a change from another window", () => {
    localStorage.setItem(TEXT_SCALE_KEY, "125");
    const store = useDisplayStore();
    store.init();
    expect(store.scale).toBe(125);
    expect(document.documentElement.dataset.textScale).toBe("125");
    store.receive(new StorageEvent("storage", { key: TEXT_SCALE_KEY, newValue: "150" }));
    expect(store.scale).toBe(150);
    expect(document.documentElement.style.getPropertyValue("--trace-root-font-size")).toBe("24px");
  });

  it("recovers from an invalid value and reports storage read failures", () => {
    localStorage.setItem(TEXT_SCALE_KEY, "137");
    let store = useDisplayStore();
    store.init();
    expect(store.scale).toBe(100);
    expect(store.warning).toContain("无效");
    expect(validTextScale("110")).toBe(110);
    expect(validTextScale("137")).toBeNull();

    setActivePinia(createPinia());
    vi.spyOn(Storage.prototype, "getItem").mockImplementationOnce(() => {
      throw new Error("storage unavailable");
    });
    store = useDisplayStore();
    store.init();
    expect(store.scale).toBe(100);
    expect(store.warning).toContain("无法读取");
  });

  it("keeps the applied value but warns when the local preference cannot be saved", async () => {
    const wrapper = mount(SettingsDisplay);
    vi.spyOn(Storage.prototype, "setItem").mockImplementationOnce(() => {
      throw new Error("read only");
    });
    await wrapper.get('[data-text-scale-option="125"]').trigger("click");
    expect(document.documentElement.dataset.textScale).toBe("125");
    expect(wrapper.get('[role="alert"]').text()).toContain("重开后可能恢复默认值");
  });
});
