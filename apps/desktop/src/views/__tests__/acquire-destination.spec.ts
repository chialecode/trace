import { beforeEach, expect, it, vi } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import AcquireView from "../AcquireView.vue";
import { api, type SourcePreview } from "../../lib/api";
import { useWorkspaceStore } from "../../stores/workspace";
import { partial } from "../../test/fixtures";
import { rule, site } from "../../test/site-fixtures";

vi.mock("../../lib/api", async (original) => {
  const m = await original<typeof import("../../lib/api")>();
  const names = [
    "credentialList",
    "sourceList",
    "sourcePreview",
    "sourceConfirm",
    "sourceCancel",
    "liveSetRecording",
    "siteCatalog",
    "deliveries",
  ];
  return {
    ...m,
    api: { ...m.api, ...Object.fromEntries(names.map((n) => [n, vi.fn()])) },
  };
});

const youtube = partial<SourcePreview>({
  preview_id: "pv",
  kind: "video",
  platform: "youtube",
  source_url: "https://www.youtube.com/watch?v=abc",
  native_key: "abc",
  title: "合成视频",
  authentication: "anonymous",
  expires_at: "2030-01-01T00:00:00Z",
  offers: [
    {
      id: "v1",
      role: "video",
      attachment_id: "video",
      required: true,
      spec: { width: 1920, height: 1080 },
    },
  ],
});

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.credentialList).mockResolvedValue({
    items: [
      { credential_id: "c-yt", platform: "youtube", subject: "油管小号", status: "valid" },
      { credential_id: "c-bili", platform: "bilibili", subject: "B站小号", status: "valid" },
    ] as never,
    next_cursor: null,
  });
  vi.mocked(api.sourceList).mockResolvedValue({ items: [] });
  vi.mocked(api.deliveries).mockResolvedValue({ items: [] });
  vi.mocked(api.siteCatalog).mockResolvedValue({
    sites: [
      site("youtube", "YouTube", {
        rules: [
          rule("youtube.entry.link", {
            url_patterns: [{ host: "youtube.com", path: "/watch", object: "entry", expand: false }],
          }),
        ],
      }),
      site("bilibili", "哔哩哔哩", {
        rules: [
          rule("bilibili.entry.link", {
            url_patterns: [
              { host: "bilibili.com", path: "/video/{id}", object: "entry", expand: false },
            ],
          }),
        ],
      }),
    ],
  });
});
async function page() {
  const pinia = createPinia();
  setActivePinia(pinia);
  useWorkspaceStore().preview = false;
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/", component: AcquireView }],
  });
  await router.push("/");
  const w = mount(AcquireView, { global: { plugins: [pinia, router] } });
  await flushPromises();
  return w;
}
const optionLabels = (w: Awaited<ReturnType<typeof page>>) =>
  w
    .get('select[aria-label="获取身份"]')
    .findAll("option")
    .map((o) => o.text());

it("offers the credentials of the site the pasted link belongs to, by the installed link rules", async () => {
  const w = await page();
  await w.get("input").setValue("https://www.youtube.com/watch?v=abc");
  expect(optionLabels(w).join()).toContain("油管小号");
  expect(optionLabels(w).join()).not.toContain("B站小号");
  await w.get("input").setValue("https://www.bilibili.com/video/BV1xx");
  expect(optionLabels(w).join()).toContain("B站小号");
  expect(optionLabels(w).join()).not.toContain("油管小号");
  await w.get("input").setValue("https://unknown.test/v/1");
  expect(optionLabels(w)).toEqual(["未登录"]);
});

it("names the eight supported sites and links to profile selection and the sites page", async () => {
  const w = await page();
  for (const name of ["微博", "小红书", "Instagram", "YouTube", "抖音", "TikTok", "哔哩哔哩"])
    expect(w.text()).toContain(name);
  expect(w.text()).toContain("主页勾选");
  expect(w.text()).toContain("支持的网站");
});

it("confirms into the library only by default, and into a folder when one is asked for", async () => {
  vi.mocked(api.sourcePreview).mockResolvedValue(youtube);
  vi.mocked(api.sourceConfirm).mockResolvedValue({ request_id: "r", job_id: "j", state: "queued" });
  const w = await page();
  await w.get("input").setValue(youtube.source_url);
  await w.get("form").trigger("submit");
  await flushPromises();
  expect(w.text()).toContain("保存到");
  const download = () => w.findAll("button").find((b) => b.text() === "下载所选资源")!;
  await download().trigger("click");
  await flushPromises();
  expect(api.sourceConfirm).toHaveBeenLastCalledWith({ preview_id: "pv", selected: ["v1"] });
  // The preview is spent after a confirm; resolve again and ask for a folder.
  await w.get("form").trigger("submit");
  await flushPromises();
  await w.get('input[type="radio"][value="folder"]').setValue(true);
  await w.get('input[aria-label="下载文件夹"]').setValue(" E:\\归档 ");
  await download().trigger("click");
  await flushPromises();
  expect(api.sourceConfirm).toHaveBeenLastCalledWith({
    preview_id: "pv",
    selected: ["v1"],
    destination: { kind: "folder", dir: "E:\\归档" },
  });
  expect(api.deliveries).toHaveBeenCalledTimes(3); // on open and after each confirm
});

it("shows the delivery progress panel only in the desktop app, not in the visual preview", async () => {
  const pinia = createPinia();
  setActivePinia(pinia);
  useWorkspaceStore().preview = true;
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/", component: AcquireView }],
  });
  await router.push("/");
  const w = mount(AcquireView, { global: { plugins: [pinia, router] } });
  await flushPromises();
  expect(w.find(".deliveries").exists()).toBe(false);
  expect(api.deliveries).not.toHaveBeenCalled();
});
