import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent } from "vue";
import { createRouter, createMemoryHistory } from "vue-router";
import { NMessageProvider } from "naive-ui";
import HomeView from "../HomeView.vue";
import App from "../../App.vue";
import { api, type EntryMetaDto, type TimelineItemDto } from "../../lib/api";
import { useWorkspaceStore } from "../../stores/workspace";
import { partial } from "../../test/fixtures";

vi.mock("@tauri-apps/api/event", () => ({ listen: vi.fn() }));
vi.mock("@tauri-apps/api/core", () => ({ isTauri: vi.fn(() => false), invoke: vi.fn() }));
vi.mock("../../lib/api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../lib/api")>();
  const names = [
    "archiveTimeline",
    "archiveEntryGet",
    "archiveEntrySync",
    "archiveTags",
    "creatorList",
    "accountList",
    "subscriptionList",
    "pluginList",
  ];
  return {
    ...original,
    api: { ...original.api, ...Object.fromEntries(names.map((n) => [n, vi.fn()])) },
  };
});

function meta(over: Record<string, unknown> = {}): EntryMetaDto {
  return partial<EntryMetaDto>({
    site: "weibo",
    canonical_url: "https://weibo.test/1/P1",
    relation: "original",
    related_key: null,
    edited_at: null,
    language: null,
    client: null,
    location_name: null,
    ip_region: null,
    content_kinds: ["text"],
    image_count: 0,
    video_count: 0,
    duration_ms: null,
    width: null,
    height: null,
    collection: null,
    category: null,
    visibility: null,
    pinned: false,
    age_restricted: false,
    sponsored: false,
    tags: [],
    mentions: [],
    stats: {
      plays: null,
      likes: null,
      comments: null,
      shares: null,
      favorites: null,
      coins: null,
      danmaku: null,
      observed_at: null,
    },
    ...over,
  });
}
function item(id: string, over: Record<string, unknown> = {}): TimelineItemDto {
  return partial<TimelineItemDto>({
    entry_id: id,
    current_revision_id: "r-" + id,
    title: "条目" + id,
    namespace: "weibo",
    kind: "post",
    published_at: "2026-10-07T00:00:00Z",
    published_precision: "second",
    save_progress: "complete",
    quality_state: "unknown",
    access_state: "available",
    ...over,
  });
}

async function setup(shell = false) {
  const pinia = createPinia();
  setActivePinia(pinia);
  useWorkspaceStore().preview = false;
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/", component: HomeView }],
  });
  await router.push("/");
  await router.isReady();
  const host = defineComponent({
    components: { HomeView, NMessageProvider },
    template: "<NMessageProvider><HomeView/></NMessageProvider>",
  });
  const wrapper = mount(shell ? App : host, {
    global: { plugins: [pinia, router], stubs: { "router-link": { template: "<a><slot /></a>" } } },
  });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.creatorList).mockResolvedValue({ items: [], next_cursor: null });
  vi.mocked(api.accountList).mockResolvedValue({ items: [], next_cursor: null });
  vi.mocked(api.subscriptionList).mockResolvedValue({ items: [], next_cursor: null });
  vi.mocked(api.archiveTags).mockResolvedValue([
    { tag: "旅行", entries: 3 },
    { tag: "摄影", entries: 1 },
  ]);
  vi.mocked(api.archiveEntryGet).mockResolvedValue({ entry: {}, revisions: [], observations: [] });
});
afterEach(() => vi.useRealTimers());

describe("unified timeline", () => {
  it("shows what each entry holds, the counters the site reported and where it came from", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({
      items: [
        item("1", {
          meta: meta({
            content_kinds: ["text", "image"],
            image_count: 3,
            ip_region: "广东",
            tags: ["旅行", "摄影"],
            stats: { plays: 12_345, likes: 5, comments: null, favorites: null },
          }),
        }),
        item("2", {
          namespace: "bilibili",
          meta: meta({
            site: "bilibili",
            content_kinds: ["video"],
            video_count: 1,
            duration_ms: 90_000,
          }),
        }),
      ],
      next_cursor: null,
    });
    const w = await setup();
    const cards = w.findAll("article.feed-card");
    const first = cards[0]!.text();
    expect(first).toContain("微博");
    expect(first).toContain("文字 + 3 张图");
    expect(first).toContain("播放 1.2万 · 点赞 5");
    expect(first).not.toContain("评论");
    expect(first).toContain("IP 属地 广东");
    expect(first).toContain("# 旅行");
    expect(cards[1]!.text()).toContain("视频 1:30");
    expect(cards[1]!.find(".duration").text()).toBe("1:30");
  });

  it("puts entries with no known publish time in their own group at the end", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({
      items: [item("1"), item("2", { published_at: null, published_precision: "unknown" })],
      next_cursor: null,
    });
    const w = await setup();
    const headings = w.findAll(".day-heading").map((h) => h.text());
    expect(headings[0]).toContain("2026-10-07");
    expect(headings.at(-1)).toContain("发布时间未知");
  });

  it("labels videos left undownloaded by the subscription and fetches one on request", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({
      items: [
        item("late", {
          save_progress: "deferred",
          meta: meta({ content_kinds: ["video"], video_count: 1 }),
        }),
        item("done", { save_progress: "complete" }),
      ],
      next_cursor: null,
    });
    vi.mocked(api.archiveEntrySync).mockResolvedValue({
      items: [{ entry_id: "late", state: "queued", groups: 1, message: null }],
    });
    const w = await setup();
    const cards = w.findAll("article.feed-card");
    expect(cards[0]!.text()).toContain("未下载（按订阅设置）");
    expect(cards[1]!.find(".entry-fetch").exists()).toBe(false);
    await cards[0]!.get(".entry-fetch button").trigger("click");
    await flushPromises();
    expect(api.archiveEntrySync).toHaveBeenCalledWith(["late"]);
    expect(w.text()).toContain("已加入下载队列");
  });

  it("says plainly when nothing could be queued", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({
      items: [item("p", { save_progress: "partial" })],
      next_cursor: null,
    });
    vi.mocked(api.archiveEntrySync).mockResolvedValue({
      items: [{ entry_id: "p", state: "nothing", groups: 0, message: null }],
    });
    const w = await setup();
    await w.get(".entry-fetch button").trigger("click");
    await flushPromises();
    expect(w.text()).toContain("没有需要下载的视频");
  });

  it("sorts by a counter, filters by tag and kind of content, and resets with the other filters", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({ items: [item("1")], next_cursor: null });
    const w = await setup();
    vi.useFakeTimers();
    await w.get('select[aria-label="排序依据"]').setValue("plays");
    await vi.advanceTimersByTimeAsync(300);
    expect(api.archiveTimeline).toHaveBeenLastCalledWith(
      expect.objectContaining({ sort: "plays", oldest: false }),
    );
    expect(w.get(".day-heading").text()).toContain("按播放量（从高到低）");
    await w.get('select[aria-label="时间排序"]').setValue(true);
    await vi.advanceTimersByTimeAsync(300);
    expect(api.archiveTimeline).toHaveBeenLastCalledWith(
      expect.objectContaining({ sort: "plays", oldest: true }),
    );
    expect(w.get(".day-heading").text()).toContain("从低到高");
    await w.get('select[aria-label="内容形式"]').setValue("video");
    await w.get('input[aria-label="话题标签"]').setValue(" 旅行 ");
    await vi.advanceTimersByTimeAsync(300);
    expect(api.archiveTimeline).toHaveBeenLastCalledWith(
      expect.objectContaining({ sort: "plays", contentKind: "video", tag: "旅行" }),
    );
    await w
      .findAll("button")
      .find((b) => b.text() === "清除筛选")!
      .trigger("click");
    await vi.advanceTimersByTimeAsync(300);
    const last = vi.mocked(api.archiveTimeline).mock.calls.at(-1)![0];
    expect(last.sort).toBeUndefined();
    expect(last.tag).toBeUndefined();
    expect(last.contentKind).toBeUndefined();
    expect(w.findAll(".day-heading")[0]!.text()).toContain("2026-10-07");
  });

  it("offers frequent tags as suggestions and the undownloaded state as a filter", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({ items: [], next_cursor: null });
    const w = await setup();
    expect(w.findAll("datalist option").map((o) => o.attributes("value"))).toEqual([
      "旅行",
      "摄影",
    ]);
    const states = w.get('select[aria-label="保存状态"]').findAll("option");
    expect(states.map((o) => o.text())).toContain("未下载（按订阅设置）");
  });

  it("shows the unified fields of the selected entry in the detail column", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({ items: [item("1")], next_cursor: null });
    vi.mocked(api.archiveEntryGet).mockResolvedValue({
      entry: { current_revision_id: "r-1" },
      revisions: [],
      observations: [],
      meta: meta({
        relation: "repost",
        related_key: "ORIG-9",
        client: "某客户端",
        ip_region: "上海",
        pinned: true,
        collection: "旅行合集",
        mentions: ["小明"],
        stats: { plays: 100, likes: 7, comments: null, observed_at: "2026-10-08T00:00:00Z" },
      }),
    });
    const w = await setup();
    const panel = w.get(".entry-meta");
    expect(panel.text()).toContain("转发");
    expect(panel.text()).toContain("关联 ORIG-9");
    expect(panel.text()).toContain("播放 100 · 点赞 7");
    expect(panel.text()).not.toContain("评论");
    expect(panel.text()).toContain("IP 属地 上海 · 来自 某客户端");
    expect(panel.text()).toContain("合集 旅行合集");
    expect(panel.text()).toContain("置顶");
    expect(panel.text()).toContain("@小明");
    expect(panel.text()).toContain("https://weibo.test/1/P1");
  });

  it("renders hostile titles and tags as text", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({
      items: [item("1", { title: "<img src=x onerror=1>", meta: meta({ tags: ["<b>粗</b>"] }) })],
      next_cursor: null,
    });
    const w = await setup();
    expect(w.find(".feed-card img[src='x']").exists()).toBe(false);
    expect(w.find(".entry-tags b").exists()).toBe(false);
    expect(w.text()).toContain("<b>粗</b>");
  });
});

describe("navigation", () => {
  it("lists every installed site in the sidebar in the fixed order, and the new pages", async () => {
    vi.mocked(api.archiveTimeline).mockResolvedValue({ items: [], next_cursor: null });
    vi.mocked(api.pluginList).mockResolvedValue({
      items: [
        "tiktok",
        "weibo",
        "bilibili",
        "xiaohongshu",
        "instagram",
        "youtube",
        "x",
        "douyin",
      ].map((platform) => ({ plugin_id: platform + "-1", platform })) as never,
      next_cursor: null,
    });
    const w = await setup(true);
    await flushPromises();
    const sites = w.findAll(".platform-nav").map((b) => b.text());
    expect(sites).toHaveLength(8);
    expect(sites[0]).toContain("Bilibili");
    expect(sites[2]).toContain("微博");
    expect(sites.at(-1)).toContain("TikTok");
    const nav = w.findAll(".main-nav a").map((a) => a.attributes("aria-label"));
    for (const label of ["主页勾选", "资源管理器", "支持的网站", "设置"])
      expect(nav).toContain(label);
  });
});
