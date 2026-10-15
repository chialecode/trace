import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ExplorerView from "../ExplorerView.vue";
import { api } from "../../lib/api";
import { listing, node, row } from "../../test/site-fixtures";

vi.mock("../../lib/api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../lib/api")>();
  const names = [
    "explorerTree",
    "explorerList",
    "explorerOpen",
    "explorerReveal",
    "downloadSync",
    "downloadForget",
    "downloadExport",
    "archiveEntrySync",
    "openExternal",
  ];
  return {
    ...original,
    api: { ...original.api, ...Object.fromEntries(names.map((n) => [n, vi.fn()])) },
  };
});

const stubs = { "router-link": { template: "<a><slot /></a>" } };
const roots = [
  node("资源库", { node: "lib", kind: "library", count: 3, expandable: true }),
  node("下载文件夹", { node: "dl", kind: "downloads", expandable: true }),
];
let wrapper: VueWrapper;

async function open() {
  wrapper = mount(ExplorerView, { global: { stubs }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}
const rlo = String.fromCharCode(0x202e);
const find = (label: string) =>
  wrapper.findAll("button").find((b) => b.text() === label || b.attributes("aria-label") === label);

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.explorerTree).mockImplementation(async (n) =>
    n === "lib"
      ? {
          nodes: [
            node("微博", { node: "lib/s:weibo", kind: "site", count: 2, expandable: true }),
            node("哔哩哔哩", { node: "lib/s:bilibili", kind: "site", count: 1, expandable: false }),
          ],
          truncated: false,
        }
      : { nodes: n ? [] : roots, truncated: false },
  );
  vi.mocked(api.explorerList).mockResolvedValue(listing([], { path: "资源库" }));
});
afterEach(() => {
  wrapper.unmount();
  document.body.innerHTML = "";
  vi.useRealTimers();
});

describe("tree", () => {
  it("opens the first root, expands lazily, and moves by keyboard like a tree", async () => {
    await open();
    expect(api.explorerList).toHaveBeenCalledWith(
      expect.objectContaining({ node: "lib", offset: 0, limit: 100 }),
    );
    const items = () => wrapper.findAll('[role="treeitem"]');
    const labels = () => items().map((i) => i.get(".label").text());
    expect(labels()).toEqual(["资源库3", "下载文件夹"]);
    expect(items()[0]!.attributes("aria-selected")).toBe("true");
    // ArrowRight on a closed folder expands it and loads its children once.
    await items()[0]!.trigger("keydown", { key: "ArrowRight" });
    await flushPromises();
    expect(api.explorerTree).toHaveBeenCalledWith("lib");
    expect(labels()).toEqual(["资源库3", "微博2", "哔哩哔哩1", "下载文件夹"]);
    expect(items()[0]!.attributes("aria-expanded")).toBe("true");
    // ArrowDown moves focus, ArrowLeft on a leaf goes to its parent, ArrowLeft again collapses.
    items()[1]!.element.dispatchEvent(new FocusEvent("focus"));
    await items()[1]!.trigger("keydown", { key: "ArrowDown" });
    await items()[2]!.trigger("keydown", { key: "ArrowLeft" });
    expect(document.activeElement).toBe(items()[0]!.element);
    await items()[0]!.trigger("keydown", { key: "ArrowLeft" });
    expect(items()).toHaveLength(2);
    expect(vi.mocked(api.explorerTree).mock.calls.filter((c) => c[0] === "lib")).toHaveLength(1);
    // Enter selects.
    await items()[1]!.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(api.explorerList).toHaveBeenLastCalledWith(expect.objectContaining({ node: "dl" }));
  });

  it("explains a tree that cannot be read and recovers on retry", async () => {
    vi.mocked(api.explorerTree).mockRejectedValueOnce(new Error("后台未连接"));
    await open();
    expect(wrapper.get('[role="alert"]').text()).toContain("后台未连接");
    vi.mocked(api.explorerTree).mockResolvedValue({ nodes: roots, truncated: false });
    await wrapper.get('[role="alert"] button').trigger("click");
    await flushPromises();
    expect(wrapper.findAll('[role="treeitem"]')).toHaveLength(2);
  });

  it("renders a hostile folder name as text with its hidden characters made visible", async () => {
    vi.mocked(api.explorerTree).mockResolvedValue({
      nodes: [node(`<b onclick=x>夹${rlo}`, { node: "lib", expandable: false })],
      truncated: false,
    });
    await open();
    const item = wrapper.get('[role="treeitem"]');
    expect(item.find("b").exists()).toBe(false);
    expect(item.get(".label").text()).toContain("<b onclick=x>夹�");
  });
});

describe("list", () => {
  const files = listing(
    [
      row("1", "视频一.mp4", {
        size: 5 * 1024 * 1024,
        duration_ms: 90_000,
        width: 1920,
        height: 1080,
        site: "weibo",
        author: "作者甲",
        published_at: "2026-10-01T00:00:00Z",
        entry_id: "e1",
        blob_id: "sha256:" + "a".repeat(64),
        state: "complete",
        can_open: true,
        path: "资源库/微博/视频一.mp4",
      }),
      row("2", "子文件夹", { kind: "folder", node: "lib/s:weibo", size: null }),
    ],
    { path: "资源库 / 微博", total: 250 },
  );

  it("shows size, duration, dimensions, site and author, and pages by a hundred", async () => {
    vi.mocked(api.explorerList).mockResolvedValue(files);
    await open();
    const first = wrapper.findAll("tbody tr")[0]!.text();
    expect(first).toContain("视频一.mp4");
    expect(first).toContain("5.0 MB");
    expect(first).toContain("1:30");
    expect(first).toContain("1920×1080");
    expect(first).toContain("微博");
    expect(first).toContain("作者甲");
    expect(wrapper.text()).toContain("第 1–2 条，共 250 条");
    expect(wrapper.get(".address").text()).toBe("资源库 / 微博");
    vi.mocked(api.explorerList).mockResolvedValue({ ...files, offset: 100 });
    await find("下一页")!.trigger("click");
    await flushPromises();
    expect(api.explorerList).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 100 }));
    expect(find("上一页")!.attributes("disabled")).toBeUndefined();
  });

  it("stays usable at a hundred thousand entries: only one page is asked for at a time", async () => {
    vi.mocked(api.explorerList).mockResolvedValue({ ...files, total: 100_000 });
    await open();
    expect(wrapper.text()).toContain("共 100000 条");
    for (let page = 1; page <= 3; page++) await find("下一页")!.trigger("click");
    await flushPromises();
    expect(api.explorerList).toHaveBeenLastCalledWith(
      expect.objectContaining({ offset: 300, limit: 100 }),
    );
    // No call ever asks for more than one page.
    for (const [args] of vi.mocked(api.explorerList).mock.calls)
      expect(args.limit).toBeLessThanOrEqual(100);
  });

  it("sorts by a column header, toggling direction, and goes back to the first page", async () => {
    vi.mocked(api.explorerList).mockResolvedValue(files);
    await open();
    await find("下一页")!.trigger("click");
    await flushPromises();
    await wrapper
      .findAll("th button")
      .find((b) => b.text().startsWith("大小"))!
      .trigger("click");
    await flushPromises();
    expect(api.explorerList).toHaveBeenLastCalledWith(
      expect.objectContaining({ sort: "size", descending: false, offset: 0 }),
    );
    await wrapper
      .findAll("th button")
      .find((b) => b.text().startsWith("大小"))!
      .trigger("click");
    await flushPromises();
    expect(api.explorerList).toHaveBeenLastCalledWith(
      expect.objectContaining({ sort: "size", descending: true }),
    );
    expect(
      wrapper
        .findAll("th")
        .find((t) => t.text().startsWith("大小"))!
        .attributes("aria-sort"),
    ).toBe("descending");
  });

  it("filters by name after a pause, not in the middle of an input-method composition", async () => {
    vi.mocked(api.explorerList).mockResolvedValue(files);
    await open();
    vi.useFakeTimers();
    const input = wrapper.get('input[aria-label="按名称筛选"]');
    const before = vi.mocked(api.explorerList).mock.calls.length;
    (input.element as HTMLInputElement).value = "lu";
    await input.trigger("input", { isComposing: true });
    await vi.advanceTimersByTimeAsync(600);
    expect(api.explorerList).toHaveBeenCalledTimes(before);
    await input.setValue("旅行");
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();
    expect(api.explorerList).toHaveBeenLastCalledWith(
      expect.objectContaining({ query: "旅行", offset: 0 }),
    );
    await wrapper.get('select[aria-label="按状态筛选"]').setValue("deferred");
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();
    expect(api.explorerList).toHaveBeenLastCalledWith(
      expect.objectContaining({ query: "旅行", state: "deferred" }),
    );
  });

  it("steps into a folder row and keeps only the answer to the last request", async () => {
    vi.mocked(api.explorerList).mockResolvedValue(files);
    await open();
    let late!: (v: ReturnType<typeof listing>) => void;
    vi.mocked(api.explorerList).mockReturnValueOnce(
      new Promise((resolve) => {
        late = resolve;
      }),
    );
    await wrapper.get("button.name-button").trigger("click");
    vi.mocked(api.explorerList).mockResolvedValueOnce(
      listing([row("n", "较新的结果")], { path: "新" }),
    );
    await wrapper.findAll("th button")[0]!.trigger("click");
    await new Promise((r) => setTimeout(r, 20));
    await flushPromises();
    late(listing([row("o", "过期的结果")], { path: "旧" }));
    await flushPromises();
    expect(wrapper.text()).toContain("较新的结果");
    expect(wrapper.text()).not.toContain("过期的结果");
  });

  it("shows empty, filtered-empty, truncated and error states", async () => {
    await open();
    expect(wrapper.text()).toContain("这里还没有内容");
    vi.mocked(api.explorerList).mockResolvedValue(listing([], { truncated: true }));
    await wrapper.get('input[aria-label="按名称筛选"]').setValue("无");
    await flushPromises();
    await new Promise((r) => setTimeout(r, 300));
    await flushPromises();
    expect(wrapper.text()).toContain("没有符合筛选条件的内容");
    expect(wrapper.text()).toContain("只读取了前一部分");
    vi.mocked(api.explorerList).mockRejectedValue(new Error("读取失败"));
    await find("刷新")!.trigger("click");
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain("读取失败");
  });
});

describe("malicious names and tampered rows", () => {
  it("never renders a file name as markup and flags hidden characters", async () => {
    vi.mocked(api.explorerList).mockResolvedValue(
      listing([
        row("a", `<img src=x onerror=alert(1)>.mp4`, { path: "x" }),
        row("b", `invoice${rlo}fdp.exe`, { path: "y" }),
        row("c", "..\\..\\Windows\\system32\\cmd.exe", { path: "z" }),
        row("d", `a\u0000b`, { path: "w" }),
      ]),
    );
    await open();
    expect(wrapper.find("tbody img").exists()).toBe(false);
    const cells = wrapper.findAll("tbody tr").map((r) => r.text());
    expect(cells[0]).toContain("<img src=x onerror=alert(1)>.mp4");
    expect(cells[1]).toContain("invoice�fdp.exe");
    expect(cells[1]).toContain("含隐藏字符");
    expect(cells[2]).toContain("..\\..\\Windows\\system32\\cmd.exe");
    expect(cells[3]).toContain("a�b");
    // A name is only a label: acting on it sends the row's own location and relative path.
    expect(api.explorerOpen).not.toHaveBeenCalled();
  });
});

describe("row actions", () => {
  const download = row("f1", "照片.jpg", {
    kind: "image",
    state: "changed",
    entry_id: "e9",
    location_id: "loc1",
    rel_path: "微博\\作者\\照片.jpg",
    path: "D:\\下载\\微博\\作者\\照片.jpg",
    can_open: true,
    can_reveal: true,
    can_sync: true,
    can_forget: true,
  });
  async function openDownloads() {
    vi.mocked(api.explorerList).mockResolvedValue(listing([download], { path: "下载文件夹" }));
    await open();
  }

  it("opens and reveals with the location and relative path of the row, not its label", async () => {
    await openDownloads();
    await find("打开 照片.jpg")!.trigger("click");
    await flushPromises();
    expect(api.explorerOpen).toHaveBeenCalledWith("loc1", "微博\\作者\\照片.jpg");
    await find("在文件夹中显示 照片.jpg")!.trigger("click");
    await flushPromises();
    expect(api.explorerReveal).toHaveBeenCalledWith("loc1", "微博\\作者\\照片.jpg");
  });

  it("opens a library file through its verified object", async () => {
    vi.mocked(api.explorerList).mockResolvedValue(
      listing([
        row("v", "视频.mp4", {
          blob_id: "sha256:" + "b".repeat(64),
          can_open: true,
          path: "库/视频.mp4",
        }),
      ]),
    );
    await open();
    await find("打开 视频.mp4")!.trigger("click");
    await flushPromises();
    expect(api.openExternal).toHaveBeenCalledWith("sha256:" + "b".repeat(64));
    expect(api.explorerOpen).not.toHaveBeenCalled();
  });

  it("copies the path and says so when the clipboard is unavailable", async () => {
    await openDownloads();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    await find("复制路径 照片.jpg")!.trigger("click");
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith("D:\\下载\\微博\\作者\\照片.jpg");
    expect(wrapper.text()).toContain("路径已复制");
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    await find("复制路径 照片.jpg")!.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("不能写入剪贴板");
  });

  it("syncs to the library and reports the outcome in plain words", async () => {
    await openDownloads();
    vi.mocked(api.downloadSync).mockResolvedValue({
      items: [{ entry_id: "e9", state: "already_in_library", files: 1, message: null }],
    });
    await find("同步到资源库 照片.jpg")!.trigger("click");
    await flushPromises();
    expect(api.downloadSync).toHaveBeenCalledWith(["e9"]);
    expect(wrapper.text()).toContain("资源库里已有相同的内容，没有重复复制");
  });

  it("forgets the record only after a second click and says the file stays", async () => {
    await openDownloads();
    vi.mocked(api.downloadForget).mockResolvedValue({ removed: 1 });
    await find("忘记记录 照片.jpg")!.trigger("click");
    expect(api.downloadForget).not.toHaveBeenCalled();
    await find("忘记记录 照片.jpg")!.trigger("click");
    await flushPromises();
    expect(api.downloadForget).toHaveBeenCalledWith("e9", "loc1");
    expect(wrapper.text()).toContain("文件本身没有被改动");
  });

  it("fetches an undownloaded entry and batches fetch or export over the ticked rows", async () => {
    vi.mocked(api.explorerList).mockResolvedValue(
      listing([
        row("a", "条目甲", { entry_id: "e1", state: "deferred", node: "lib/e:e1", path: "库/甲" }),
        row("b", "条目乙", { entry_id: "e2", state: "complete", node: "lib/e:e2", path: "库/乙" }),
      ]),
    );
    await open();
    vi.mocked(api.archiveEntrySync).mockResolvedValue({
      items: [{ entry_id: "e1", state: "queued", groups: 1, message: null }],
    });
    await find("补下载 条目甲")!.trigger("click");
    await flushPromises();
    expect(api.archiveEntrySync).toHaveBeenCalledWith(["e1"]);
    expect(wrapper.text()).toContain("已加入下载队列");
    expect(find("补下载 条目乙")).toBeUndefined();
    await wrapper.get('input[aria-label="选择 条目甲"]').setValue(true);
    await wrapper.get('input[aria-label="选择 条目乙"]').setValue(true);
    vi.mocked(api.downloadExport).mockResolvedValue({
      folder: {} as never,
      items: [
        { entry_id: "e1", state: "exported", files: [], message: null },
        { entry_id: "e2", state: "nothing", files: [], message: null },
      ],
    });
    await find("导出到默认下载文件夹")!.trigger("click");
    await flushPromises();
    expect(api.downloadExport).toHaveBeenCalledWith(["e1", "e2"]);
    expect(wrapper.text()).toContain("已写入 1 条");
    expect(wrapper.text()).toContain("还没有可导出的文件 1 条");
  });

  it("previews an image through the local media address only", async () => {
    vi.mocked(api.explorerList).mockResolvedValue(
      listing([
        row("p", "封面.jpg", {
          kind: "image",
          blob_id: "sha256:" + "c".repeat(64),
          path: "库/封面.jpg",
        }),
        row("q", "坏.jpg", { kind: "image", blob_id: "not-a-blob", path: "库/坏.jpg" }),
      ]),
    );
    await open();
    await find("预览 封面.jpg")!.trigger("click");
    expect(wrapper.get(".preview img").attributes("src")).toBe(
      "http://trace-media.localhost/blob/" + "c".repeat(64),
    );
    await find("预览 坏.jpg")!.trigger("click");
    expect(wrapper.find(".preview img").exists()).toBe(false);
    expect(wrapper.get(".preview").text()).toContain("暂时不能预览");
  });
});
