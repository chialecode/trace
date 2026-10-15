import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { NCheckbox } from "naive-ui";
import { beforeEach, it, expect, vi } from "vitest";
import UpgradesView from "../UpgradesView.vue";
import AcquisitionHistory from "../../components/AcquisitionHistory.vue";
import LiveRecording from "../../components/LiveRecording.vue";
import MediaBrowser from "../../components/MediaBrowser.vue";
import ArchiveDocument from "../../components/ArchiveDocument.vue";
import { api, type UpgradeCandidate, type MediaDto } from "../../lib/api";
import { useWorkspaceStore } from "../../stores/workspace";
vi.mock("../../lib/api", async (original) => {
  const source = await original<typeof import("../../lib/api")>();
  return {
    ...source,
    api: {
      ...source.api,
      upgradeList: vi.fn(),
      upgradeDecide: vi.fn(),
      creatorList: vi.fn(),
      archiveAcquisitions: vi.fn(),
      liveFragments: vi.fn(),
    },
  };
});
const candidate = (id: string): UpgradeCandidate => ({
  sequence: 1,
  candidate_id: id,
  entry_id: "entry",
  revision_id: "revision",
  state: "pending",
  reason: "verified",
  title: "同一内容",
  namespace: "douyin",
  auth_state: "authenticated",
  acquired_at: "2026-10-08T01:00:00Z",
  complete: true,
  expected: {
    revision_id: "revision",
    selection_digest: "old-selection-" + id,
    credential_generation: 2,
  },
  resources: [
    {
      media_id: "video",
      role: "video",
      required: true,
      old: {
        variant_id: "old",
        bytes: 100,
        spec: { width: 1280, height: 720, fps: 30 },
        auth_state: "anonymous",
        pinned: false,
      },
      new: {
        variant_id: "new",
        bytes: 200,
        spec: { width: 1920, height: 1080, fps: 60, bitrate_bps: 8000000 },
      },
    },
  ],
});
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.creatorList).mockResolvedValue({ items: [], next_cursor: null });
});
async function mountPage(preview = false) {
  const pinia = createPinia();
  setActivePinia(pinia);
  useWorkspaceStore().preview = preview;
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/", component: UpgradesView }],
  });
  await router.push("/");
  const wrapper = mount(UpgradesView, { global: { plugins: [pinia, router] } });
  await flushPromises();
  return wrapper;
}

it("upgrade_page_requires_explicit_selection_and_preserves_failed_comparison", async () => {
  const first = candidate("first"),
    second = candidate("second");
  first.reason = "content_edited_requires_decision";
  vi.mocked(api.upgradeList).mockResolvedValue({ items: [first, second], total: 2 });
  const wrapper = await mountPage();
  expect(api.upgradeDecide).not.toHaveBeenCalled();
  expect(wrapper.text()).toContain("登录获取");
  expect(wrapper.text()).toContain("未登录获取");
  expect(wrapper.text()).toContain("1920 × 1080");
  expect(wrapper.text()).toContain("8000 kbps");
  expect(wrapper.text()).toContain("来源正文已编辑");
  wrapper.findAllComponents(NCheckbox)[0]!.vm.$emit("update:checked", true);
  await flushPromises();
  vi.mocked(api.upgradeDecide).mockRejectedValueOnce(new Error("version_conflict"));
  await wrapper
    .findAll("button")
    .find((b) => b.text() === "接受所选升级")!
    .trigger("click");
  await flushPromises();
  expect(api.upgradeDecide).toHaveBeenLastCalledWith([
    { candidate_id: "first", decision: "accept", expected: first.expected },
  ]);
  expect(wrapper.text()).toContain("旧资源已保留");
  expect(wrapper.findAll("article")).toHaveLength(2);
  expect(api.upgradeList).toHaveBeenCalledTimes(1);
  vi.mocked(api.upgradeDecide).mockResolvedValueOnce({});
  vi.mocked(api.upgradeList).mockResolvedValueOnce({ items: [second], total: 1 });
  await wrapper
    .findAll("article")[0]!
    .findAll("button")
    .find((b) => b.text() === "保留两份")!
    .trigger("click");
  await flushPromises();
  expect(api.upgradeDecide).toHaveBeenLastCalledWith([
    { candidate_id: "first", decision: "keep_both", expected: first.expected },
  ]);
  expect(wrapper.findAll("article")).toHaveLength(1);
  wrapper.unmount();
  vi.clearAllMocks();
  const preview = await mountPage(true);
  expect(api.upgradeList).not.toHaveBeenCalled();
  expect(api.upgradeDecide).not.toHaveBeenCalled();
  preview.unmount();
});

it("pending_candidates_never_become_default_playback_and_keep_resource_identity_labels", async () => {
  const item = (id: string, approval: string): MediaDto => ({
    media_id: "video",
    variant_id: id,
    role: "video",
    position: 0,
    representation: id,
    blob_id: "sha256:" + (id === "old" ? "a" : id === "new" ? "b" : "c").repeat(64),
    size: 100,
    available: true,
    mime: "video/mp4",
    approval_state: approval,
    authentication: {
      state: id === "old" ? "anonymous" : "authenticated",
      states: [id === "old" ? "anonymous" : "authenticated"],
    },
  });
  const pending = item("new", "pending"),
    rejected = item("rejected", "rejected"),
    old = { ...item("old", "automatic"), selected: true };
  const wrapper = mount(MediaBrowser, { props: { media: [pending, rejected, old] } });
  expect((wrapper.get("select").element as HTMLSelectElement).value).toBe("old");
  expect(wrapper.text()).toContain("未登录获取");
  expect(wrapper.text()).toContain("待确认候选");
  const node = wrapper.get("video").element;
  node.currentTime = 8;
  await wrapper.setProps({ media: [{ ...pending }, old] });
  expect(wrapper.get("video").element).toBe(node);
  expect(node.currentTime).toBe(8);
  await wrapper.setProps({ media: [pending, rejected] });
  expect(wrapper.find("video").exists()).toBe(false);
  expect((wrapper.get("select").element as HTMLSelectElement).value).toBe("");
  wrapper.unmount();
  const image = { ...pending, mime: "image/png", attachment_id: "image" };
  const doc = mount(ArchiveDocument, {
    props: {
      body: "",
      media: [image],
      document: {
        version: 1,
        omissions: [],
        blocks: [{ kind: "resource", attachment_id: "image", caption: "原图" }],
      },
    },
  });
  expect(doc.find("img").exists()).toBe(false);
  expect(doc.text()).toContain("未保存");
  await doc.setProps({ media: [image, { ...old, mime: "image/png", attachment_id: "image" }] });
  expect(doc.get("img").attributes("src")).toContain("a".repeat(64));
  doc.unmount();
});

it("acquisition_history_is_lazy_paginated_and_does_not_relabel_old_results", async () => {
  const row = (id: string, state: "authenticated" | "anonymous") => ({
    sequence: id === "old" ? 1 : 2,
    acquisition_id: id,
    auth_state: state,
    started_at: "2026-10-08T01:00:00Z",
    finished_at: "2026-10-08T01:00:01Z",
    outcome: "acquired",
    current_credential_available: false,
  });
  vi.mocked(api.archiveAcquisitions)
    .mockResolvedValueOnce({
      items: [row("new", "authenticated")],
      next_before: 2,
      summary: { state: "mixed", states: ["anonymous", "authenticated"] },
    })
    .mockResolvedValueOnce({
      items: [row("old", "anonymous")],
      summary: { state: "mixed", states: ["anonymous", "authenticated"] },
    });
  const wrapper = mount(AcquisitionHistory, {
    props: { entryId: "entry" },
    global: { stubs: { RouterLink: true } },
  });
  expect(api.archiveAcquisitions).not.toHaveBeenCalled();
  (wrapper.get("details").element as HTMLDetailsElement).open = true;
  await wrapper.get("details").trigger("toggle");
  await flushPromises();
  await wrapper
    .findAll("button")
    .find((b) => b.text() === "加载更早获取")!
    .trigger("click");
  await flushPromises();
  expect(wrapper.findAll("li")).toHaveLength(2);
  expect(wrapper.text()).toContain("未登录获取");
  expect(wrapper.text()).toContain("登录获取");
  expect(api.archiveAcquisitions).toHaveBeenLastCalledWith("entry", 2);
  let finish!: (v: Awaited<ReturnType<typeof api.archiveAcquisitions>>) => void;
  vi.mocked(api.archiveAcquisitions).mockImplementationOnce(
    () => new Promise((done) => (finish = done)),
  );
  await wrapper
    .findAll("button")
    .find((b) => b.text() === "刷新历史")!
    .trigger("click");
  await wrapper.setProps({ entryId: "other" });
  finish({
    items: [row("late", "authenticated")],
    summary: { state: "authenticated", states: ["authenticated"] },
  });
  await flushPromises();
  expect(wrapper.findAll("li")).toHaveLength(0);
  wrapper.unmount();
});

it("live_fragment_labels_keep_mixed_unknown_and_actual_bitrate", async () => {
  vi.mocked(api.liveFragments).mockResolvedValue({
    items: [
      {
        sequence: 1,
        state: "sealed",
        authentication: "authenticated",
        actual_spec: { width: 1920, height: 1080, bitrate_bps: 5000000, fps: 30, codec: "h264" },
      },
      { sequence: 2, state: "gap", authentication: "unknown", actual_spec: {} },
    ],
    total: 2,
  });
  const wrapper = mount(LiveRecording, {
    props: {
      live: {
        session_id: "session",
        state: "sealed",
        generation: 1,
        manual_stopped: true,
        started_at: "t",
      },
      fragments: [],
    },
  });
  (wrapper.get("details").element as HTMLDetailsElement).open = true;
  await wrapper.get("details").trigger("toggle");
  await flushPromises();
  expect(wrapper.text()).toContain("登录获取");
  expect(wrapper.text()).toContain("身份未知");
  expect(wrapper.text()).toContain("1920 × 1080");
  expect(wrapper.text()).toContain("5000 kbps");
  expect(wrapper.text()).toContain("码率未知");
  expect(wrapper.text()).toContain("登录后不补录过去");
  expect(wrapper.findAll("li")).toHaveLength(2);
  wrapper.unmount();
});
