import { mount, flushPromises } from "@vue/test-utils";
import { describe, it, expect, vi, beforeEach } from "vitest";
import MediaBrowser from "../MediaBrowser.vue";
import { api, mediaUrl } from "../../lib/api";
vi.mock("@tauri-apps/api/core", () => ({ invoke: vi.fn() }));
const blob = `sha256:${"a".repeat(64)}`;
const media = {
  media_id: "m",
  variant_id: "v",
  role: "video",
  position: 1,
  blob_id: blob,
  mime: "video/mp4",
  available: true,
  size: 120,
};
beforeEach(() => vi.restoreAllMocks());
describe("controlled media browsing", () => {
  it("uses a blob handle and never accepts a path", async () => {
    const open = vi.spyOn(api, "openExternal").mockResolvedValue();
    const wrapper = mount(MediaBrowser, { props: { media: [media] } });
    expect(wrapper.get("video").attributes("src")).toBe(mediaUrl(blob));
    await wrapper.get("button").trigger("click");
    await flushPromises();
    expect(open).toHaveBeenCalledWith(blob);
    expect(wrapper.text()).toContain("已交给系统媒体程序");
    expect(mediaUrl("../private.exe")).toBe("");
    wrapper.unmount();
  });
  it("does not offer a player for absent files and explains unsupported codecs", async () => {
    const wrapper = mount(MediaBrowser, { props: { media: [{ ...media, available: false }] } });
    expect(wrapper.find("video").exists()).toBe(false);
    expect(wrapper.text()).toContain("副本缺失");
    await wrapper.setProps({ media: [media] });
    await wrapper.get("video").trigger("error");
    expect(wrapper.get('[role="status"]').text()).toContain("无法播放此编码");
    vi.spyOn(api, "openExternal").mockRejectedValue(new Error("disconnected"));
    await wrapper.get("button").trigger("click");
    await flushPromises();
    expect(wrapper.get('[role="status"]').text()).toContain("后台未连接");
    wrapper.unmount();
  });
});
