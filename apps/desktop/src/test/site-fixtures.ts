/** Synthetic site rules, explorer rows and profile items for the view specs. */
import type {
  ExplorerNode,
  ExplorerRow,
  ListView,
  ProfileItem,
  ProfilePage,
  SiteView,
} from "../lib/api";
import { partial } from "./fixtures";

export function rule(
  id: string,
  over: Record<string, unknown> = {},
  declared: Record<string, unknown> = {},
) {
  return {
    declaration: {
      id,
      site: id.split(".")[0],
      title: `规则 ${id}`,
      description: "合成规则说明",
      entry: "subscription",
      content_kinds: ["video"],
      url_patterns: [{ host: "example.test", path: "/u/{id}", object: "account", expand: false }],
      params: [],
      login: "none",
      login_note: null,
      network: "direct",
      limits: [],
      status: "implemented",
      verified_at: "2026-10-01",
      verified_scope: "合成账号",
      lane: "video",
      ...over,
    },
    declared: true,
    declared_at: "2026-10-01T00:00:00Z",
    adapter_version: "0.1.0",
    ...declared,
  };
}

export function site(id: string, title: string, over: Record<string, unknown> = {}): SiteView {
  return partial<SiteView>({
    id,
    title,
    login: "none",
    network: "direct",
    plugin_state: "enabled",
    rules: [rule(`${id}.account.video`)],
    ...over,
  });
}

export function node(name: string, over: Record<string, unknown> = {}): ExplorerNode {
  return partial<ExplorerNode>({
    node: `lib/${name}`,
    name,
    kind: "folder",
    count: null,
    expandable: false,
    ...over,
  });
}

export function row(key: string, name: string, over: Record<string, unknown> = {}): ExplorerRow {
  return partial<ExplorerRow>({
    key,
    name,
    kind: "video",
    kinds: [],
    size: 1024,
    duration_ms: null,
    width: null,
    height: null,
    published_at: null,
    site: null,
    author: null,
    state: "present",
    entry_id: null,
    blob_id: null,
    location_id: null,
    rel_path: null,
    path: "",
    node: null,
    can_open: false,
    can_reveal: false,
    can_sync: false,
    can_forget: false,
    ...over,
  });
}

export function listing(rows: ExplorerRow[], over: Record<string, unknown> = {}): ListView {
  return partial<ListView>({
    path: "下载文件夹 / 测试",
    rows,
    total: rows.length,
    offset: 0,
    truncated: false,
    ...over,
  });
}

export function profileItem(key: string, over: Record<string, unknown> = {}): ProfileItem {
  return partial<ProfileItem>({
    native_key: key,
    native_kind: "post",
    title: `内容 ${key}`,
    summary: null,
    published_at: "2026-10-01T00:00:00Z",
    published_precision: "second",
    pinned: false,
    content_kinds: ["video"],
    image_count: 0,
    video_count: 1,
    duration_ms: 60_000,
    in_library: false,
    entry_id: null,
    save_progress: null,
    ...over,
  });
}

export function profilePage(items: ProfileItem[], over: Record<string, unknown> = {}): ProfilePage {
  return partial<ProfilePage>({
    preview_id: "pv1",
    platform: "weibo",
    site_title: "微博",
    rule_id: "weibo.account.profile",
    rule_title: "微博主页",
    alternatives: [],
    account_key: "10001",
    account_name: "合成作者",
    authentication: "anonymous",
    items,
    pages_loaded: 1,
    page_limit: 10,
    total_items: items.length,
    has_more: false,
    limited: false,
    expires_at: "2030-01-01T00:00:00Z",
    ...over,
  });
}
