import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";

const invoke = vi.hoisted(() => vi.fn(async (cmd: string, args?: unknown) => ({ cmd, args })));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));

const forwarded = (command: string, args: Record<string, unknown>) =>
  ["daemon_call", { command, args }] as const;

describe("api: sites, deliveries, locations and explorer", () => {
  beforeEach(() => invoke.mockClear());

  it("timeline sort, tag and content kind travel with explicit nulls when unset", async () => {
    await api.archiveTimeline({ sort: "plays", tag: "旅行", contentKind: "video", limit: 20 });
    const sent = invoke.mock.calls.at(-1)?.[1] as { args: Record<string, unknown> };
    expect(sent.args).toMatchObject({
      sort: "plays",
      tag: "旅行",
      content_kind: "video",
      limit: 20,
    });
    await api.archiveTimeline({});
    const bare = invoke.mock.calls.at(-1)?.[1] as { args: Record<string, unknown> };
    expect(bare.args).toMatchObject({ sort: null, tag: null, content_kind: null, oldest: false });
  });

  it("a new subscription says nothing about auto download unless asked", async () => {
    await api.subscriptionCreate("a1", "from_now");
    const plain = invoke.mock.calls.at(-1)?.[1] as { args: Record<string, unknown> };
    expect("auto_download" in plain.args).toBe(false);
    await api.subscriptionCreate("a1", "from_now", undefined, 300, "video", { autoDownload: true });
    const on = invoke.mock.calls.at(-1)?.[1] as { args: Record<string, unknown> };
    expect(on.args.auto_download).toBe(true);
    await api.subscriptionUpdate("s1", { auto_download: false });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("subscription.update", { subscription_id: "s1", auto_download: false }),
    );
  });

  it("profile preview continues by preview id and downloads by native keys", async () => {
    await api.profilePreview({ url: "https://example.test/u/1", ruleId: "x.account.preview" });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("source.profile.preview", {
        url: "https://example.test/u/1",
        preview_id: null,
        rule_id: "x.account.preview",
        credential_id: null,
      }),
    );
    await api.profilePreview({ previewId: "p1" });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("source.profile.preview", {
        url: null,
        preview_id: "p1",
        rule_id: null,
        credential_id: null,
      }),
    );
    await api.profileDownload("p1", ["k1", "k2"], { kind: "folder", dir: null });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("source.profile.download", {
        preview_id: "p1",
        native_keys: ["k1", "k2"],
        target: { kind: "folder", dir: null },
      }),
    );
    await api.deliveries();
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("source.deliveries", { delivery_id: null, limit: null }),
    );
    await api.deliveryCancel("d1");
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("source.delivery.cancel", { delivery_id: "d1" }),
    );
  });

  it("one-source confirm adds a destination only when one was chosen", async () => {
    await api.sourceConfirm({ preview_id: "p", selected: ["a"] });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("source.confirm", { preview_id: "p", selected: ["a"] }),
    );
    await api.sourceConfirm({
      preview_id: "p",
      selected: ["a"],
      destination: { kind: "folder", dir: "D:/x" },
    });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("source.confirm", {
        preview_id: "p",
        selected: ["a"],
        destination: { kind: "folder", dir: "D:/x" },
      }),
    );
  });

  it("settings calls use the contract argument names", async () => {
    await api.libraryMigrate("E:/lib");
    expect(invoke).toHaveBeenLastCalledWith(...forwarded("library.migrate", { to_path: "E:/lib" }));
    await api.libraryReleaseOld("loc");
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("library.release_old", { location_id: "loc" }),
    );
    await api.downloadSettingsSet({ sidecar: true });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("download.settings_set", { default_dir: null, sidecar: true }),
    );
    await api.networkSet({ scope: "youtube", mode: "manual", proxy_url: "http://127.0.0.1:7890" });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("network.set", {
        proxy_url: "http://127.0.0.1:7890",
        username: null,
        password: null,
        keep_login: false,
        scope: "youtube",
        mode: "manual",
      }),
    );
    await api.downloadForget("e1", "loc");
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("download.forget", { entry_id: "e1", location_id: "loc" }),
    );
    await api.archiveEntrySync(["e1"]);
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("archive.entry.sync", { entry_ids: ["e1"] }),
    );
  });

  it("the explorer sends the node it was given and trims the name filter", async () => {
    await api.explorerTree();
    expect(invoke).toHaveBeenLastCalledWith(...forwarded("explorer.tree", { node: null }));
    await api.explorerList({
      node: "lib/s:weibo",
      sort: "size",
      descending: true,
      query: "  旅行 ",
      offset: 200,
    });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("explorer.list", {
        node: "lib/s:weibo",
        sort: "size",
        descending: true,
        query: "旅行",
        kind: null,
        state: null,
        offset: 200,
        limit: 100,
      }),
    );
    await api.explorerOpen("loc", "a\\b.mp4");
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("explorer.open", { location_id: "loc", rel_path: "a\\b.mp4" }),
    );
    await api.archiveTags({ prefix: "旅", limit: 10 });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("archive.tags", {
        creator_id: null,
        account_id: null,
        namespace: null,
        prefix: "旅",
        limit: 10,
      }),
    );
  });
});
