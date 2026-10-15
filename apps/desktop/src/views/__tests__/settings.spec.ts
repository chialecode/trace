import { flushPromises, mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import SettingsView from "../SettingsView.vue";
import SettingsDownloads from "../settings/SettingsDownloads.vue";
import SettingsLibrary from "../settings/SettingsLibrary.vue";
import SettingsLogins from "../settings/SettingsLogins.vue";
import SettingsNetwork from "../settings/SettingsNetwork.vue";
import {
  api,
  type DownloadSettingsView,
  type LibraryStatus,
  type NetworkView,
} from "../../lib/api";
import { partial } from "../../test/fixtures";
import { site } from "../../test/site-fixtures";

vi.mock("../../lib/api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../lib/api")>();
  const names = [
    "libraryStatus",
    "libraryMigrate",
    "librarySwitch",
    "libraryCancel",
    "libraryReleaseOld",
    "downloadSettings",
    "downloadSettingsSet",
    "downloadCheck",
    "networkGet",
    "networkSet",
    "siteCatalog",
    "credentialList",
  ];
  return {
    ...original,
    api: { ...original.api, ...Object.fromEntries(names.map((n) => [n, vi.fn()])) },
  };
});

const stubs = { "router-link": { template: "<a><slot /></a>" } };
const location = (path: string, over: Record<string, unknown> = {}) => ({
  location_id: "loc-" + path,
  path,
  state: "active",
  created_at: "2026-10-01T00:00:00Z",
  free_bytes: 5 * 1024 ** 3,
  exists: true,
  ...over,
});
const status = (over: Record<string, unknown> = {}) =>
  partial<LibraryStatus>({
    active: location("D:\\TRACE资源库"),
    old_kept: [],
    migration: null,
    ...over,
  });
const migration = (state: string, over: Record<string, unknown> = {}) => ({
  migration_id: "m1",
  from_path: "D:\\TRACE资源库",
  to_path: "E:\\新库",
  state,
  total_items: 10,
  done_items: 4,
  total_bytes: 1000,
  done_bytes: 400,
  message: null,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:01:00Z",
  switched_at: null,
  ...over,
});
const button = (w: ReturnType<typeof mount>, label: string) =>
  w.findAll("button").find((b) => b.text() === label);

beforeEach(() => {
  vi.resetAllMocks();
});
afterEach(() => vi.useRealTimers());

describe("library location", () => {
  it("shows the current library and refuses an empty target before calling the daemon", async () => {
    vi.mocked(api.libraryStatus).mockResolvedValue(status());
    const w = mount(SettingsLibrary);
    await flushPromises();
    expect(w.text()).toContain("D:\\TRACE资源库");
    expect(w.text()).toContain("5.0 GB");
    await button(w, "开始迁移")!.trigger("click");
    expect(api.libraryMigrate).not.toHaveBeenCalled();
    expect(w.text()).toContain("请先填写新位置");
  });

  it("starts a move, tells the old library stays, and offers the switch once verified", async () => {
    vi.mocked(api.libraryStatus).mockResolvedValue(status());
    vi.mocked(api.libraryMigrate).mockResolvedValue(status({ migration: migration("ready") }));
    const w = mount(SettingsLibrary);
    await flushPromises();
    await w.get('input[aria-label="新资源库位置"]').setValue("  E:\\新库 ");
    await button(w, "开始迁移")!.trigger("click");
    await flushPromises();
    expect(api.libraryMigrate).toHaveBeenCalledWith("E:\\新库");
    expect(w.text()).toContain("已校验，等待切换");
    expect(w.text()).toContain("旧位置在你确认前保持完整可用");
    vi.mocked(api.librarySwitch).mockResolvedValue(
      status({ active: location("E:\\新库"), migration: migration("switched") }),
    );
    await button(w, "切换到新位置")!.trigger("click");
    await flushPromises();
    expect(api.librarySwitch).toHaveBeenCalledTimes(1);
    expect(w.text()).toContain("E:\\新库");
    expect(button(w, "切换到新位置")).toBeUndefined();
  });

  it("follows a running move and stops following when the view goes away", async () => {
    vi.useFakeTimers();
    vi.mocked(api.libraryStatus).mockResolvedValue(status({ migration: migration("copying") }));
    const w = mount(SettingsLibrary);
    await flushPromises();
    expect(w.text()).toContain("正在复制");
    expect(w.text()).toContain("4 / 10 项");
    await vi.advanceTimersByTimeAsync(1600);
    expect(api.libraryStatus).toHaveBeenCalledTimes(2);
    w.unmount();
    await vi.advanceTimersByTimeAsync(5000);
    expect(api.libraryStatus).toHaveBeenCalledTimes(2);
  });

  it("releases an old library only after a second, explicit click", async () => {
    const old = location("C:\\旧库", { state: "old_kept" });
    vi.mocked(api.libraryStatus).mockResolvedValue(status({ old_kept: [old] }));
    vi.mocked(api.libraryReleaseOld).mockResolvedValue(status());
    const w = mount(SettingsLibrary);
    await flushPromises();
    await button(w, "释放旧位置")!.trigger("click");
    expect(api.libraryReleaseOld).not.toHaveBeenCalled();
    await button(w, "确认释放（TRACE 不再引用）")!.trigger("click");
    await flushPromises();
    expect(api.libraryReleaseOld).toHaveBeenCalledWith(old.location_id);
  });

  it("reports a failed move without pretending it switched, and a read error with retry", async () => {
    vi.mocked(api.libraryStatus).mockResolvedValueOnce(
      status({ migration: migration("failed", { message: "目标磁盘空间不足" }) }),
    );
    const w = mount(SettingsLibrary);
    await flushPromises();
    expect(w.text()).toContain("迁移失败");
    expect(w.text()).toContain("目标磁盘空间不足");
    expect(button(w, "切换到新位置")).toBeUndefined();
    expect(button(w, "开始迁移")).toBeTruthy();
    vi.mocked(api.libraryStatus).mockRejectedValueOnce(new Error("断开"));
    w.unmount();
    const again = mount(SettingsLibrary);
    await flushPromises();
    expect(again.get('[role="alert"]').text()).toContain("无法读取资源库位置");
  });
});

describe("download location", () => {
  const settings = (over: Record<string, unknown> = {}) =>
    partial<DownloadSettingsView>({
      default_dir: "D:\\下载",
      sidecar: false,
      recent: [
        {
          location_id: "d1",
          path: "D:\\下载",
          is_default: true,
          last_used_at: "2026-10-02T00:00:00Z",
          free_bytes: 1024 ** 3,
          exists: true,
        },
      ],
      ...over,
    });

  it("saves a new default folder, trimming it, and keeps old files where they are", async () => {
    vi.mocked(api.downloadSettings).mockResolvedValue(settings());
    vi.mocked(api.downloadSettingsSet).mockResolvedValue(settings({ default_dir: "E:\\归档" }));
    const w = mount(SettingsDownloads);
    await flushPromises();
    const input = w.get('input[aria-label="默认下载文件夹"]');
    expect((input.element as HTMLInputElement).value).toBe("D:\\下载");
    await input.setValue(" E:\\归档 ");
    await button(w, "保存")!.trigger("click");
    await flushPromises();
    expect(api.downloadSettingsSet).toHaveBeenCalledWith({ default_dir: "E:\\归档" });
    expect(w.text()).toContain("之前的文件不会被移动");
  });

  it("toggles the same-name json and checks a folder without touching files", async () => {
    vi.mocked(api.downloadSettings).mockResolvedValue(settings());
    vi.mocked(api.downloadSettingsSet).mockResolvedValue(settings({ sidecar: true }));
    vi.mocked(api.downloadCheck).mockResolvedValue({
      checked: 12,
      present: 10,
      changed: 1,
      missing: 1,
      problems: [],
    });
    const w = mount(SettingsDownloads);
    await flushPromises();
    await w.get('input[type="checkbox"]').setValue(true);
    await flushPromises();
    expect(api.downloadSettingsSet).toHaveBeenCalledWith({ sidecar: true });
    await button(w, "检查文件")!.trigger("click");
    await flushPromises();
    expect(api.downloadCheck).toHaveBeenCalledWith({ locationId: "d1" });
    expect(w.text()).toContain("检查了 12 个文件：完好 10，被修改 1，缺失 1");
  });

  it("states the naming rule and shows a rejected folder as the daemon's own message", async () => {
    vi.mocked(api.downloadSettings).mockResolvedValue(settings());
    vi.mocked(api.downloadSettingsSet).mockRejectedValue(new Error("该文件夹不可写"));
    const w = mount(SettingsDownloads);
    await flushPromises();
    expect(w.text()).toContain("站点/作者/发布日期 标题 [内容ID].扩展名");
    await w.get('input[aria-label="默认下载文件夹"]').setValue("Z:\\没有");
    await button(w, "保存")!.trigger("click");
    await flushPromises();
    expect(w.text()).toContain("该文件夹不可写");
    expect((w.get('input[aria-label="默认下载文件夹"]').element as HTMLInputElement).value).toBe(
      "D:\\下载",
    );
  });
});

describe("network and proxy", () => {
  const view = (over: Record<string, unknown> = {}) =>
    partial<NetworkView>({
      settings: [],
      system: { enabled: true, address: "127.0.0.1:7890" },
      sites: [
        {
          site: "youtube",
          title: "YouTube",
          advice: "proxy_recommended",
          source: "default",
          mode: "system",
          route: "proxy",
          description: "跟随系统代理",
        },
        {
          site: "bilibili",
          title: "哔哩哔哩",
          advice: "direct",
          source: "default",
          mode: "direct",
          route: "direct",
          description: "国内站点直连",
        },
      ],
      ...over,
    });

  it("shows how each site connects now and which saved choice applies", async () => {
    vi.mocked(api.networkGet).mockResolvedValue(view());
    const w = mount(SettingsNetwork);
    await flushPromises();
    expect(w.text()).toContain("已开启（127.0.0.1:7890）");
    const rows = w.findAll("tbody tr");
    expect(rows).toHaveLength(3);
    expect(rows[1]!.text()).toContain("走代理");
    expect(rows[1]!.text()).toContain("默认");
    expect(rows[2]!.text()).toContain("直连");
  });

  it("requires an address for a manual proxy and a complete login", async () => {
    vi.mocked(api.networkGet).mockResolvedValue(view());
    const w = mount(SettingsNetwork);
    await flushPromises();
    await w.get('select[aria-label="YouTube的连接方式"]').setValue("manual");
    await w.get('button[aria-label="保存YouTube的连接方式"]').trigger("click");
    expect(api.networkSet).not.toHaveBeenCalled();
    expect(w.text()).toContain("手动代理需要填写地址");
    await w.get('input[aria-label="YouTube的代理地址"]').setValue("http://127.0.0.1:7890");
    await w.get('input[aria-label="YouTube的代理用户名"]').setValue("u");
    await w.get('button[aria-label="保存YouTube的连接方式"]').trigger("click");
    expect(api.networkSet).not.toHaveBeenCalled();
    expect(w.text()).toContain("用户名和密码需要一起填写");
  });

  it("sends the proxy login once and never keeps the password in the page", async () => {
    vi.mocked(api.networkGet).mockResolvedValue(view());
    vi.mocked(api.networkSet).mockResolvedValue(
      view({
        settings: [
          { scope: "youtube", mode: "manual", proxy_url: "http://127.0.0.1:7890", has_login: true },
        ],
      }),
    );
    const w = mount(SettingsNetwork);
    await flushPromises();
    await w.get('select[aria-label="YouTube的连接方式"]').setValue("manual");
    await w.get('input[aria-label="YouTube的代理地址"]').setValue(" http://127.0.0.1:7890 ");
    await w.get('input[aria-label="YouTube的代理用户名"]').setValue("user");
    await w.get('input[aria-label="YouTube的代理密码"]').setValue("secret-pass");
    await w.get('button[aria-label="保存YouTube的连接方式"]').trigger("click");
    await flushPromises();
    expect(api.networkSet).toHaveBeenCalledWith({
      scope: "youtube",
      mode: "manual",
      proxy_url: "http://127.0.0.1:7890",
      username: "user",
      password: "secret-pass",
      keep_login: false,
    });
    expect(w.html()).not.toContain("secret-pass");
    expect((w.get('input[aria-label="YouTube的代理密码"]').element as HTMLInputElement).value).toBe(
      "",
    );
    expect(w.get('input[aria-label="YouTube的代理用户名"]').attributes("placeholder")).toContain(
      "已保存用户名",
    );
    // Saving again with the fields left empty keeps the saved login.
    await w.get('button[aria-label="保存YouTube的连接方式"]').trigger("click");
    await flushPromises();
    expect(vi.mocked(api.networkSet).mock.calls.at(-1)?.[0]).toMatchObject({
      username: null,
      password: null,
      keep_login: true,
    });
  });

  it("falls back to the default by removing the saved choice", async () => {
    vi.mocked(api.networkGet).mockResolvedValue(view());
    vi.mocked(api.networkSet).mockResolvedValue(view());
    const w = mount(SettingsNetwork);
    await flushPromises();
    await w.get('select[aria-label="哔哩哔哩的连接方式"]').setValue("direct");
    await w.get('select[aria-label="哔哩哔哩的连接方式"]').setValue("default");
    await w.get('button[aria-label="保存哔哩哔哩的连接方式"]').trigger("click");
    await flushPromises();
    expect(vi.mocked(api.networkSet).mock.calls.at(-1)?.[0]).toMatchObject({
      scope: "bilibili",
      mode: "default",
      proxy_url: null,
    });
  });
});

describe("site logins and risk tips", () => {
  it("lists every site's login need and the credentials already imported", async () => {
    vi.mocked(api.siteCatalog).mockResolvedValue({
      sites: [
        site("instagram", "Instagram", { login: "required" }),
        site("bilibili", "哔哩哔哩", { login: "optional" }),
      ],
    });
    vi.mocked(api.credentialList).mockResolvedValue({
      items: [
        { credential_id: "c1", platform: "bilibili", subject: "小号", status: "valid" },
      ] as never,
      next_cursor: null,
    });
    const w = mount(SettingsLogins, { global: { stubs } });
    await flushPromises();
    const rows = w.findAll("tbody tr");
    expect(rows[0]!.text()).toContain("哔哩哔哩");
    expect(rows[0]!.text()).toContain("小号");
    expect(rows[0]!.text()).toContain("有效");
    expect(rows[1]!.text()).toContain("需要登录");
    expect(rows[1]!.text()).toContain("尚未导入，登录后才能使用");
    expect(w.text()).toContain("只保存在本机的系统凭证库");
    expect(w.text()).toContain("不会当作“没有更新”或“已删除”");
  });
});

describe("settings page", () => {
  it("brings the four sections together", async () => {
    vi.mocked(api.libraryStatus).mockResolvedValue(status());
    vi.mocked(api.downloadSettings).mockResolvedValue(
      partial<DownloadSettingsView>({ default_dir: "D:\\下载", sidecar: false, recent: [] }),
    );
    vi.mocked(api.networkGet).mockResolvedValue(
      partial<NetworkView>({ settings: [], sites: [], system: { enabled: false, address: null } }),
    );
    vi.mocked(api.siteCatalog).mockResolvedValue({ sites: [] });
    vi.mocked(api.credentialList).mockResolvedValue({ items: [], next_cursor: null });
    const w = mount(SettingsView, { global: { plugins: [createPinia()], stubs } });
    await flushPromises();
    for (const title of [
      "显示与文字",
      "资源库位置",
      "下载位置与命名",
      "网络与代理",
      "站点登录与风险提示",
    ])
      expect(w.text()).toContain(title);
    expect(w.findAll("nav button")).toHaveLength(5);
  });
});
