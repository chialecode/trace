import { mount, flushPromises } from "@vue/test-utils";
import { defineComponent } from "vue";
import { NMessageProvider } from "naive-ui";
import { it, expect, vi } from "vitest";
import ManagementView from "../ManagementView.vue";
import { api } from "../../lib/api";
import { partial } from "../../test/fixtures";

vi.mock("vue-router", () => ({ useRoute: () => ({ query: {} }) }));
vi.mock("../../lib/api", async (original) => {
  const source = await original<typeof import("../../lib/api")>();
  return {
    ...source,
    api: {
      ...source.api,
      creatorList: vi.fn(),
      creatorMerge: vi.fn(),
      creatorMergePreview: vi.fn(),
      creatorOperations: vi.fn(),
      identityCandidates: vi.fn(),
      importInspect: vi.fn(),
      importStart: vi.fn(),
      importStatus: vi.fn(),
      resourceCompare: vi.fn(),
      resourceCompareDecide: vi.fn(),
      liveReplayCandidates: vi.fn(),
      liveReplayLinks: vi.fn(),
      liveReplayLink: vi.fn(),
      liveReplayReject: vi.fn(),
      qualityListFiltered: vi.fn(),
      qualityPinEntry: vi.fn(),
      qualityConfigure: vi.fn(),
      pluginList: vi.fn(),
      pluginSetState: vi.fn(),
      pluginReleases: vi.fn(),
      pluginCapabilities: vi.fn(),
      toolStatus: vi.fn(),
      workGroupList: vi.fn(),
      workBatchPreview: vi.fn(),
    },
  };
});

it("management_view_offers_every_explicit_management_area_without_automatic_actions", async () => {
  vi.mocked(api.creatorList).mockResolvedValue({
    items: [partial({ creator_id: "cre-1", display_name: "合成作者" })],
    next_cursor: null,
  });
  vi.mocked(api.creatorOperations).mockResolvedValue({ items: [], next_after: null });
  vi.mocked(api.identityCandidates).mockResolvedValue({ items: [], next_after: null });
  vi.mocked(api.importStatus).mockResolvedValue({
    batches: [],
    batch: null,
    items: [],
    next_after: null,
  });
  vi.mocked(api.pluginList).mockResolvedValue({ items: [], next_cursor: null });
  vi.mocked(api.pluginReleases).mockResolvedValue({ items: [] });
  vi.mocked(api.pluginCapabilities).mockResolvedValue({ contracts: [], active: [] });
  vi.mocked(api.toolStatus).mockResolvedValue(
    partial({ tool: "yt-dlp", locked: "2026.08.19", ready: true, source: "bundled" }),
  );
  const wrapper = mount(
    defineComponent({
      components: { NMessageProvider, ManagementView },
      template: "<NMessageProvider><ManagementView/></NMessageProvider>",
    }),
  );
  await flushPromises();
  const text = wrapper.text();
  for (const label of [
    "身份关系",
    "本地导入",
    "重复与比较",
    "回放关联",
    "质量筛选",
    "作品聚合",
    "插件与能力",
    "外部工具",
  ]) {
    expect(text).toContain(label);
  }
  expect(text).toContain("不会自动合并、删除或回收");
  expect(text).toContain("原地索引的外部文件不会被移动");
  expect(text).toContain("没有待确认候选");
  expect(api.pluginSetState).not.toHaveBeenCalled();
  expect(api.creatorMerge).not.toHaveBeenCalled();

  vi.mocked(api.workGroupList).mockResolvedValue({ items: [], next_after: null });
  vi.mocked(api.workBatchPreview).mockResolvedValue(
    partial({ token: "batch-preview", entry_ids: ["entry-1", "entry-2"] }),
  );
  await wrapper.get('[data-name="works"]').trigger("click");
  await flushPromises();
  await wrapper.get('input[placeholder="条目 ID，用逗号分隔"]').setValue("entry-1, entry-2");
  await wrapper
    .get('input[placeholder="批量添加的标签（留空则重新评估质量）"]')
    .setValue("合成标签");
  const preview = wrapper.findAll("button").find((button) => button.text() === "预览影响");
  expect(preview).toBeDefined();
  await preview!.trigger("click");
  await flushPromises();
  expect(api.workBatchPreview).toHaveBeenCalledWith(["entry-1", "entry-2"], {
    tags_add: ["合成标签"],
  });
  wrapper.unmount();
});

it("management_view_confirms_previews_and_routes_compare_replay_and_quality_actions", async () => {
  vi.mocked(api.creatorList).mockResolvedValue({
    items: [partial({ creator_id: "cre-1", display_name: "合成作者" })],
    next_cursor: null,
  });
  vi.mocked(api.creatorOperations).mockResolvedValue({ items: [], next_after: null });
  vi.mocked(api.identityCandidates).mockResolvedValue({ items: [], next_after: null });
  vi.mocked(api.importStatus).mockResolvedValue({
    batches: [],
    batch: null,
    items: [],
    next_after: null,
  });
  vi.mocked(api.pluginList).mockResolvedValue({ items: [], next_cursor: null });
  vi.mocked(api.pluginReleases).mockResolvedValue({ items: [] });
  vi.mocked(api.pluginCapabilities).mockResolvedValue({ contracts: [], active: [] });
  vi.mocked(api.toolStatus).mockResolvedValue(partial({ tool: "yt-dlp", ready: true }));
  vi.mocked(api.importInspect).mockResolvedValue(
    partial({
      files: 2,
      bytes: 9,
      unreadable: 0,
      duplicates: [],
      links_skipped: 0,
      preview_token: "preview-1",
    }),
  );
  vi.mocked(api.importStart).mockResolvedValue(partial({ batch_id: "batch-1", total_items: 2 }));
  vi.mocked(api.resourceCompare).mockResolvedValue(
    partial({ left: { entry_id: "e1" }, right: { entry_id: "e2" } }),
  );
  vi.mocked(api.liveReplayCandidates).mockResolvedValue({
    items: [
      partial({
        entry_id: "r1",
        title: "回放",
        coverage: {
          complete: false,
          reason: "replay is shorter than the recording",
          recorded_ranges: [{ start_ms: 0, end_ms: 18000 }],
          unique_recorded_ranges: [{ start_ms: 8000, end_ms: 18000 }],
        },
        quality: "incomparable",
        can_replay_primary: false,
        comparison_reason: "回放或录制的规格不完整，质量不可比较；只能保留两者",
      }),
    ],
  });
  vi.mocked(api.liveReplayLinks).mockResolvedValue({
    items: [
      partial({
        entry_id: "saved-1",
        title: "已保存回放",
        state: "confirmed",
        decision: "keep_both",
        coverage: {
          complete: false,
          reason: "replay is shorter than the recording",
          recorded_ranges: [{ start_ms: 0, end_ms: 18000 }],
          unique_recorded_ranges: [{ start_ms: 8000, end_ms: 18000 }],
        },
        quality: { comparison: "same" },
      }),
    ],
  });
  vi.mocked(api.liveReplayLink).mockResolvedValue(
    partial({ coverage: { complete: true }, quality: "same" }),
  );
  vi.mocked(api.qualityListFiltered).mockResolvedValue({
    items: [
      partial({
        entry_id: "e1",
        state: "pending_upgrade",
        states: ["pending_upgrade"],
        reasons: ["等待选择"],
      }),
    ],
    next_after: null,
    preference: "resolution",
    recycle_wait_seconds: 30,
  });
  vi.mocked(api.qualityPinEntry).mockResolvedValue({ pinned: true });
  vi.mocked(api.creatorMergePreview).mockResolvedValue({
    impact: { source_scope: { accounts: 1 } },
    preview_token: "merge-1",
  });
  vi.mocked(api.creatorMerge).mockResolvedValue({ merged_into: "cre-2" });
  const wrapper = mount(
    defineComponent({
      components: { NMessageProvider, ManagementView },
      template: "<NMessageProvider><ManagementView/></NMessageProvider>",
    }),
  );
  await flushPromises();

  const selects = wrapper.findAllComponents({ name: "Select" });
  expect(selects.length).toBeGreaterThan(1);
  selects[0]!.vm.$emit("update:value", "cre-1");
  selects[1]!.vm.$emit("update:value", "cre-2");
  await flushPromises();
  const previewMerge = wrapper.findAll("button").find((button) => button.text() === "预览合并影响");
  await previewMerge!.trigger("click");
  await flushPromises();
  expect(api.creatorMergePreview).toHaveBeenCalledWith("cre-1", "cre-2");
  const confirmMerge = wrapper
    .findAll("button")
    .find((button) => button.text() === "按预览确认合并");
  await confirmMerge!.trigger("click");
  await flushPromises();
  expect(api.creatorMerge).toHaveBeenCalledWith("cre-1", "cre-2", "merge-1");
  selects[1]!.vm.$emit("update:value", null);
  await flushPromises();

  await wrapper.get('[data-name="imports"]').trigger("click");
  await flushPromises();
  await wrapper
    .get('input[placeholder="本机文件或目录的完整路径"]')
    .setValue("D:/synthetic-import");
  const preview = wrapper.findAll("button").find((button) => button.text() === "预览");
  await preview!.trigger("click");
  await flushPromises();
  expect(api.importInspect).toHaveBeenCalledWith("D:/synthetic-import", "managed", undefined);
  const start = wrapper.findAll("button").find((button) => button.text() === "开始导入");
  expect(start!.attributes("disabled")).toBeDefined();
  await wrapper.get(".n-checkbox").trigger("click");
  await flushPromises();
  await start!.trigger("click");
  await flushPromises();
  expect(api.importStart).toHaveBeenCalledWith(
    "D:/synthetic-import",
    "managed",
    "preview-1",
    undefined,
  );

  await wrapper.get('[data-name="compare"]').trigger("click");
  await flushPromises();
  await wrapper.get('input[placeholder="左侧条目 ID"]').setValue("e1");
  await wrapper.get('input[placeholder="右侧条目 ID"]').setValue("e2");
  const compare = wrapper.findAll("button").find((button) => button.text() === "比较");
  await compare!.trigger("click");
  await flushPromises();
  expect(api.resourceCompare).toHaveBeenCalledWith("e1", "e2");

  await wrapper.get('[data-name="replays"]').trigger("click");
  await flushPromises();
  await wrapper.get('input[placeholder="录制场次 ID"]').setValue("sess-1");
  const findReplay = wrapper.findAll("button").find((button) => button.text() === "查候选");
  await findReplay!.trigger("click");
  await flushPromises();
  expect(wrapper.text()).toContain("8000–18000 ms");
  expect(wrapper.text()).toContain("回放或录制的规格不完整");
  const inspect = wrapper.findAll("button").find((button) => button.text() === "查看比较");
  await inspect!.trigger("click");
  await flushPromises();
  const primary = wrapper.findAll("button").find((button) => button.text() === "设回放为主");
  expect(primary).toBeDefined();
  expect(primary!.attributes("disabled")).toBeDefined();
  const cancel = wrapper.findAll("button").find((button) => button.text() === "取消");
  await cancel!.trigger("click");
  await flushPromises();
  expect(api.liveReplayLink).not.toHaveBeenCalled();
  await inspect!.trigger("click");
  await flushPromises();
  const link = wrapper.findAll("button").find((button) => button.text() === "关联并保留两者");
  await link!.trigger("click");
  await flushPromises();
  expect(api.liveReplayLink).toHaveBeenCalledWith("sess-1", "r1", "keep_both");
  expect(wrapper.text()).toContain("已保存回放");

  await wrapper.get('[data-name="quality"]').trigger("click");
  await flushPromises();
  const filter = wrapper.findAll("button").find((button) => button.text() === "筛选");
  const qualitySelect = wrapper
    .findAllComponents({ name: "Select" })
    .find((select) =>
      ((select.props("options") as { value: string }[]) ?? []).some(
        (option) => option.value === "pending_upgrade",
      ),
    );
  expect(qualitySelect).toBeDefined();
  qualitySelect!.vm.$emit("update:value", "pending_upgrade");
  await flushPromises();
  await filter!.trigger("click");
  await flushPromises();
  expect(api.qualityListFiltered).toHaveBeenCalledWith("pending_upgrade", undefined, 100);
  const pin = wrapper.findAll("button").find((button) => button.text() === "固定");
  await pin!.trigger("click");
  await flushPromises();
  expect(api.qualityPinEntry).toHaveBeenCalledWith("e1", true);
  wrapper.unmount();
});
