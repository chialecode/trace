import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import SitesView from "../SitesView.vue";
import { api, type Recognition } from "../../lib/api";
import { partial } from "../../test/fixtures";
import { rule, site } from "../../test/site-fixtures";

vi.mock("../../lib/api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../lib/api")>();
  return { ...original, api: { ...original.api, siteCatalog: vi.fn(), siteRecognize: vi.fn() } };
});

const stubs = { "router-link": { template: "<a><slot /></a>" } };
const open = async () => {
  const w = mount(SitesView, { global: { stubs } });
  await flushPromises();
  return w;
};

beforeEach(() => vi.resetAllMocks());

describe("supported sites page", () => {
  it("lists the sites in the fixed order with their login, network and rule status", async () => {
    vi.mocked(api.siteCatalog).mockResolvedValue({
      sites: [
        site("youtube", "YouTube", {
          login: "optional",
          network: "proxy_recommended",
          rules: [
            rule("youtube.account.video", {
              status: "partial",
              limits: ["直播回放暂不支持"],
              verified_at: null,
              verified_scope: null,
            }),
            rule("youtube.entry.link", {
              entry: "link",
              status: "unimplemented",
              limits: ["尚未实现"],
            }),
          ],
        }),
        site("bilibili", "哔哩哔哩"),
      ],
    });
    const w = await open();
    const cards = w.findAll("section.site-card");
    expect(cards.map((c) => c.attributes("aria-label"))).toEqual(["哔哩哔哩", "YouTube"]);
    const youtube = cards[1]!;
    expect(youtube.text()).toContain("登录后更完整");
    expect(youtube.text()).toContain("建议使用代理");
    expect(youtube.text()).toContain("部分实现");
    expect(youtube.text()).toContain("未实现");
    expect(youtube.text()).toContain("直播回放暂不支持");
    expect(youtube.text()).toContain("尚未用真实站点验证过");
    expect(youtube.text()).toContain("已实现 0 / 2");
    expect(cards[0]!.text()).toContain("最近一次真实验证：2026-10-01");
  });

  it("shows a disabled module as display-only and never claims it can be used", async () => {
    vi.mocked(api.siteCatalog).mockResolvedValue({
      sites: [site("tiktok", "TikTok", { plugin_state: "disabled" })],
    });
    const w = await open();
    expect(w.get('[role="status"]').text()).toContain("未启用");
  });

  it("shows an error with a retry, and an empty state when nothing is installed", async () => {
    vi.mocked(api.siteCatalog).mockRejectedValueOnce(new Error("后台未连接"));
    const w = await open();
    expect(w.get('[role="alert"]').text()).toContain("后台未连接");
    vi.mocked(api.siteCatalog).mockResolvedValueOnce({ sites: [] });
    await w.get('[role="alert"] button').trigger("click");
    await flushPromises();
    expect(w.text()).toContain("还没有安装任何站点模块");
  });

  it("renders text from the catalog as text, not markup", async () => {
    vi.mocked(api.siteCatalog).mockResolvedValue({
      sites: [
        site("x", "X", {
          rules: [rule("x.account.video", { title: "<img src=x onerror=alert(1)>" })],
        }),
      ],
    });
    const w = await open();
    expect(w.find("img").exists()).toBe(false);
    expect(w.text()).toContain("<img src=x onerror=alert(1)>");
  });
});

describe("link recognizer", () => {
  const found = partial<Recognition>({
    shown: "example.test/u/1",
    site: "weibo",
    site_title: "微博",
    object: "account",
    rules: [
      {
        rule_id: "weibo.account.video",
        title: "微博订阅",
        entry: "subscription",
        object: "account",
        status: "partial",
        login: "optional",
        network: "direct",
        lane: "video",
        captures: [],
      },
    ],
    needs_expansion: true,
    message: null,
  });

  it("recognizes a pasted link and lists the rules without visiting the site", async () => {
    vi.mocked(api.siteCatalog).mockResolvedValue({ sites: [] });
    vi.mocked(api.siteRecognize).mockResolvedValue(found);
    const w = await open();
    await w.get('input[aria-label="要识别的链接"]').setValue(" https://example.test/u/1 ");
    await w
      .findAll("button")
      .find((b) => b.text() === "识别")!
      .trigger("click");
    await flushPromises();
    expect(api.siteRecognize).toHaveBeenCalledWith("https://example.test/u/1");
    const text = w.get(".found").text();
    expect(text).toContain("微博");
    expect(text).toContain("账号主页");
    expect(text).toContain("短链接，需要先展开才能确认");
    expect(text).toContain("部分实现");
    expect(text).toContain("不会访问网站");
  });

  it("does not submit on the Enter that only confirms an input-method candidate", async () => {
    vi.mocked(api.siteCatalog).mockResolvedValue({ sites: [] });
    vi.mocked(api.siteRecognize).mockResolvedValue(found);
    const w = await open();
    const input = w.get('input[aria-label="要识别的链接"]');
    await input.setValue("https://example.test/u/1");
    await input.trigger("keydown", { key: "Enter", isComposing: true });
    await input.trigger("keydown", { key: "Enter", keyCode: 229 });
    expect(api.siteRecognize).not.toHaveBeenCalled();
    await input.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(api.siteRecognize).toHaveBeenCalledTimes(1);
  });

  it("says plainly when the link belongs to no supported site, and when recognition fails", async () => {
    vi.mocked(api.siteCatalog).mockResolvedValue({ sites: [] });
    vi.mocked(api.siteRecognize).mockResolvedValueOnce(
      partial<Recognition>({ site: null, rules: [], message: "这个链接不属于已支持的网站。" }),
    );
    const w = await open();
    const input = w.get('input[aria-label="要识别的链接"]');
    await input.setValue("https://nowhere.test/");
    await input.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(w.get(".found").text()).toContain("不属于已支持的网站");
    vi.mocked(api.siteRecognize).mockRejectedValueOnce(new Error("链接无效"));
    await input.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(w.find(".found").exists()).toBe(false);
    expect(w.text()).toContain("链接无效");
  });
});
