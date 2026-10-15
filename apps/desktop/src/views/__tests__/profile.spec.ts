import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ProfileSelectView from "../ProfileSelectView.vue";
import DeliveriesPanel from "../../components/DeliveriesPanel.vue";
import { api, type DeliveryView } from "../../lib/api";
import { partial } from "../../test/fixtures";
import { profileItem, profilePage } from "../../test/site-fixtures";

vi.mock("../../lib/api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../lib/api")>();
  const names = [
    "profilePreview",
    "profileDownload",
    "deliveries",
    "deliveryCancel",
    "credentialList",
    "downloadSettings",
  ];
  return {
    ...original,
    api: { ...original.api, ...Object.fromEntries(names.map((n) => [n, vi.fn()])) },
  };
});

const stubs = { "router-link": { template: "<a><slot /></a>" } };
let wrapper: VueWrapper;
const LINK = "https://weibo.test/u/10001";
const button = (label: string) => wrapper.findAll("button").find((b) => b.text().startsWith(label));

async function preview(
  first = profilePage([profileItem("a"), profileItem("b"), profileItem("c")]),
) {
  vi.mocked(api.profilePreview).mockResolvedValueOnce(first);
  wrapper = mount(ProfileSelectView, { global: { stubs } });
  await flushPromises();
  await wrapper.get('input[aria-label="作者主页链接"]').setValue(LINK);
  await button("预览主页")!.trigger("click");
  await flushPromises();
}
const box = (key: string) => wrapper.get(`input[aria-label="选择 内容 ${key}"]`);

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.credentialList).mockResolvedValue({ items: [], next_cursor: null });
  vi.mocked(api.downloadSettings).mockResolvedValue({
    default_dir: "D:\\下载",
    sidecar: false,
    recent: [],
  });
  vi.mocked(api.deliveries).mockResolvedValue({ items: [] });
});
afterEach(() => {
  wrapper.unmount();
  vi.useRealTimers();
});

describe("profile preview and selection", () => {
  it("previews a pasted link and lists what the profile holds", async () => {
    await preview(
      profilePage([
        profileItem("a", {
          pinned: true,
          summary: "第一条的开头",
          image_count: 3,
          video_count: 0,
          content_kinds: ["text", "image"],
        }),
        profileItem("b", { published_at: null }),
        profileItem("c", { in_library: true, save_progress: "complete" }),
      ]),
    );
    expect(api.profilePreview).toHaveBeenCalledWith({ url: LINK, credentialId: undefined });
    const text = wrapper.text();
    expect(text).toContain("合成作者");
    expect(text).toContain("微博");
    expect(text).toContain("未登录获取");
    expect(text).toContain("置顶");
    expect(text).toContain("文字 + 3 张图");
    expect(text).toContain("发布时间未知");
    expect(text).toContain("已在资源库 · 已保存");
    expect(text).toContain("已读取 1 页（最多 10 页），共 3 条");
  });

  it("keeps the selection while more pages load and skips what the library already holds", async () => {
    await preview(
      profilePage([profileItem("a"), profileItem("held", { in_library: true })], {
        has_more: true,
      }),
    );
    await box("a").trigger("click");
    vi.mocked(api.profilePreview).mockResolvedValueOnce(
      profilePage([profileItem("a"), profileItem("d"), profileItem("e")], { pages_loaded: 2 }),
    );
    await button("加载更多")!.trigger("click");
    await flushPromises();
    expect(api.profilePreview).toHaveBeenLastCalledWith({
      previewId: "pv1",
      credentialId: undefined,
    });
    expect(wrapper.findAll("ul.profile-items li")).toHaveLength(4); // "a" is not listed twice
    expect(wrapper.text()).toContain("已选 1 条");
    expect(wrapper.text()).toContain("已经读到末尾");
    await button("选中已读取的未保存内容")!.trigger("click");
    expect(wrapper.text()).toContain("已选 3 条"); // a, d, e — not the one already held
    await button("清空选择")!.trigger("click");
    expect(wrapper.text()).toContain("已选 0 条");
  });

  it("selects a range with shift-click", async () => {
    await preview(profilePage(["a", "b", "c", "d"].map((k) => profileItem(k))));
    await box("a").trigger("click");
    await box("c").trigger("click", { shiftKey: true });
    expect(wrapper.text()).toContain("已选 3 条");
    expect((box("d").element as HTMLInputElement).checked).toBe(false);
  });

  it("downloads the ticked entries to the library only, and shows what was skipped and why", async () => {
    await preview();
    await box("a").trigger("click");
    await box("c").trigger("click");
    vi.mocked(api.profileDownload).mockResolvedValue({
      delivery_id: "d1",
      accepted: 1,
      already_in_library: 0,
      skipped: [{ native_key: "c", reason: "这条内容需要登录才能下载" }],
    });
    await button("下载所选 2 条")!.trigger("click");
    await flushPromises();
    expect(api.profileDownload).toHaveBeenCalledWith("pv1", ["a", "c"], {
      kind: "library",
      dir: null,
    });
    const note = wrapper.get('[role="status"].state-box').text();
    expect(note).toContain("已接收 1 条");
    expect(note).toContain("这条内容需要登录才能下载");
    expect(wrapper.text()).toContain("已选 0 条");
    expect(api.deliveries).toHaveBeenCalledTimes(2); // read again for the new delivery
  });

  it("can also write copies to a download folder, with the default or a typed one", async () => {
    await preview();
    await box("b").trigger("click");
    await wrapper.get('input[type="radio"][value="folder"]').setValue(true);
    expect(wrapper.text()).toContain("留空用默认：D:\\下载");
    vi.mocked(api.profileDownload).mockResolvedValue({
      delivery_id: "d2",
      accepted: 1,
      already_in_library: 0,
      skipped: [],
    });
    await button("下载所选")!.trigger("click");
    await flushPromises();
    expect(api.profileDownload).toHaveBeenLastCalledWith("pv1", ["b"], {
      kind: "folder",
      dir: null,
    });
    await box("b").trigger("click");
    await wrapper.get('input[aria-label="下载文件夹"]').setValue(" E:\\拿来 ");
    await button("下载所选")!.trigger("click");
    await flushPromises();
    expect(api.profileDownload).toHaveBeenLastCalledWith("pv1", ["b"], {
      kind: "folder",
      dir: "E:\\拿来",
    });
  });

  it("does not download without a selection, and shows a refused request without losing the selection", async () => {
    await preview();
    expect(button("下载所选 0 条")!.attributes("disabled")).toBeDefined();
    await box("a").trigger("click");
    vi.mocked(api.profileDownload).mockRejectedValue(new Error("预览已过期，请重新预览"));
    await button("下载所选 1 条")!.trigger("click");
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain("预览已过期");
    expect(wrapper.text()).toContain("已选 1 条");
  });

  it("stops at a challenge or rate limit and says so, instead of looking like an empty profile", async () => {
    vi.mocked(api.profilePreview).mockRejectedValueOnce(new Error("站点要求验证，已停止"));
    wrapper = mount(ProfileSelectView, { global: { stubs } });
    await flushPromises();
    await wrapper.get('input[aria-label="作者主页链接"]').setValue(LINK);
    await button("预览主页")!.trigger("click");
    await flushPromises();
    const alert = wrapper.get('[role="alert"]').text();
    expect(alert).toContain("站点要求验证，已停止");
    expect(alert).toContain("不会换个方式硬闯");
    expect(wrapper.text()).not.toContain("这个主页下没有读到内容");
  });

  it("reads an empty profile as empty, notes the read limit, and lets another rule be chosen", async () => {
    await preview(
      profilePage([], {
        limited: true,
        has_more: false,
        alternatives: [{ rule_id: "weibo.account.alt", title: "另一种读取方式" }],
      }),
    );
    expect(wrapper.text()).toContain("这个主页下没有读到内容");
    expect(wrapper.text()).toContain("已到读取上限");
    vi.mocked(api.profilePreview).mockResolvedValueOnce(profilePage([profileItem("z")]));
    await wrapper.get('select[aria-label="换一种读取方式"]').setValue("weibo.account.alt");
    await flushPromises();
    expect(api.profilePreview).toHaveBeenLastCalledWith({
      url: LINK,
      ruleId: "weibo.account.alt",
      credentialId: undefined,
    });
    expect(wrapper.text()).toContain("内容 z");
  });

  it("does not start on the Enter that confirms an input-method candidate", async () => {
    vi.mocked(api.profilePreview).mockResolvedValue(profilePage([]));
    wrapper = mount(ProfileSelectView, { global: { stubs } });
    await flushPromises();
    const input = wrapper.get('input[aria-label="作者主页链接"]');
    await input.setValue(LINK);
    await input.trigger("keydown", { key: "Enter", isComposing: true });
    expect(api.profilePreview).not.toHaveBeenCalled();
    await input.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(api.profilePreview).toHaveBeenCalledTimes(1);
  });

  it("renders titles and summaries from the site as plain text", async () => {
    await preview(
      profilePage([
        profileItem("x", {
          title: "<script>alert(1)</script>标题",
          summary: "<img src=x onerror=1>",
        }),
      ]),
    );
    expect(wrapper.find("script").exists()).toBe(false);
    expect(wrapper.find("ul.profile-items img").exists()).toBe(false);
    expect(wrapper.text()).toContain("<script>alert(1)</script>标题");
  });
});

describe("deliveries panel", () => {
  const delivery = (state: string, items: Record<string, unknown>[] = []) =>
    partial<DeliveryView>({
      delivery_id: "d1",
      kind: "profile",
      platform: "weibo",
      source: "weibo.test/u/10001",
      target: { kind: "folder", dir: "D:\\下载" },
      state,
      created_at: "2026-10-01T00:00:00Z",
      finished_at: null,
      items,
    });

  it("follows a running delivery until it finishes, then stops reading", async () => {
    vi.useFakeTimers();
    vi.mocked(api.deliveries)
      .mockResolvedValueOnce({
        items: [
          delivery("running", [
            { native_key: "a", title: "甲", state: "queued", files: 0, message: null },
          ]),
        ],
      })
      .mockResolvedValue({
        items: [
          delivery("done", [
            { native_key: "a", title: "甲", state: "exported", files: 2, message: null },
            {
              native_key: "b",
              title: "乙",
              state: "failed",
              files: 0,
              message: "站点限流，已停止",
            },
          ]),
        ],
      });
    wrapper = mount(DeliveriesPanel, { props: { pollMs: 1000 } });
    await flushPromises();
    expect(wrapper.text()).toContain("进行中");
    expect(wrapper.text()).toContain("资源库 + 下载文件夹（D:\\下载）");
    await vi.advanceTimersByTimeAsync(1100);
    expect(wrapper.text()).toContain("已结束");
    expect(wrapper.text()).toContain("完成 1，部分 0，失败 1，进行中 0");
    expect(wrapper.text()).toContain("已写入下载文件夹");
    expect(wrapper.text()).toContain("站点限流，已停止");
    await vi.advanceTimersByTimeAsync(5000);
    expect(api.deliveries).toHaveBeenCalledTimes(2);
  });

  it("cancels a running delivery and says finished work stays", async () => {
    vi.useFakeTimers();
    vi.mocked(api.deliveries).mockResolvedValue({ items: [delivery("running")] });
    vi.mocked(api.deliveryCancel).mockResolvedValue({});
    wrapper = mount(DeliveriesPanel);
    await flushPromises();
    await button("取消")!.trigger("click");
    await flushPromises();
    expect(api.deliveryCancel).toHaveBeenCalledWith("d1");
    expect(wrapper.text()).toContain("已经写入的内容会保留");
  });

  it("shows the empty and the error state", async () => {
    vi.mocked(api.deliveries).mockResolvedValueOnce({ items: [] });
    wrapper = mount(DeliveriesPanel);
    await flushPromises();
    expect(wrapper.text()).toContain("还没有批量下载");
    vi.mocked(api.deliveries).mockRejectedValueOnce(new Error("后台未连接"));
    await button("刷新")!.trigger("click");
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain("后台未连接");
  });
});
