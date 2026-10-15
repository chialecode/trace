/**
 * Plain-Chinese names for the sites, their rules and the content they hold. The daemon sends
 * identifiers; every sentence the user reads is made here. Pure functions, covered by Vitest.
 */
import type { RuleEntry } from "./generated/RuleEntry";
import type { RuleLogin } from "./generated/RuleLogin";
import type { RuleNetwork } from "./generated/RuleNetwork";
import type { RuleObject } from "./generated/RuleObject";
import type { RuleStatus } from "./generated/RuleStatus";

/** The eight sites in the order the interface lists them. */
export const SITES: { id: string; label: string; mark: string }[] = [
  { id: "bilibili", label: "哔哩哔哩", mark: "bili" },
  { id: "douyin", label: "抖音", mark: "♪" },
  { id: "weibo", label: "微博", mark: "微" },
  { id: "xiaohongshu", label: "小红书", mark: "书" },
  { id: "youtube", label: "YouTube", mark: "▶" },
  { id: "x", label: "X", mark: "𝕏" },
  { id: "instagram", label: "Instagram", mark: "◎" },
  { id: "tiktok", label: "TikTok", mark: "♫" },
];

export function siteLabel(id: string | null | undefined): string {
  if (!id) return "未知站点";
  const site = id.split(":")[0] ?? id;
  return SITES.find((s) => s.id === site)?.label ?? site;
}

export function siteMark(id: string): string {
  return SITES.find((s) => s.id === id)?.mark ?? "·";
}

export const RULE_STATUS_LABELS: Record<RuleStatus, string> = {
  implemented: "已实现",
  partial: "部分实现",
  unimplemented: "未实现",
  retired: "已停用",
};

export function ruleStatusLabel(status: RuleStatus | string): string {
  return RULE_STATUS_LABELS[status as RuleStatus] ?? status;
}

export function ruleStatusTone(
  status: RuleStatus | string,
): "success" | "warning" | "error" | "default" {
  if (status === "implemented") return "success";
  if (status === "partial") return "warning";
  if (status === "unimplemented") return "error";
  return "default";
}

export function loginLabel(login: RuleLogin | string): string {
  return (
    (
      { none: "无需登录", optional: "登录后更完整", required: "需要登录" } as Record<string, string>
    )[login] ?? login
  );
}

export function networkLabel(network: RuleNetwork | string): string {
  return (
    (
      {
        direct: "国内直连",
        proxy_recommended: "建议使用代理",
      } as Record<string, string>
    )[network] ?? network
  );
}

export function entryLabel(entry: RuleEntry | string): string {
  return (
    ({ subscription: "订阅", preview: "预览勾选", link: "单链接" } as Record<string, string>)[
      entry
    ] ?? entry
  );
}

export function objectLabel(object: RuleObject | string): string {
  return ({ account: "账号主页", entry: "单个内容" } as Record<string, string>)[object] ?? object;
}

export const CONTENT_KIND_LABELS: Record<string, string> = {
  text: "文字",
  image: "图片",
  live_photo: "实况图",
  video: "视频",
  audio: "音频",
  article: "文章",
  live: "直播",
  other: "其他",
};

export function contentKindLabel(kind: string): string {
  return CONTENT_KIND_LABELS[kind] ?? kind;
}

/** "图文 3 图"、"视频 1:30"：一条内容里有什么，混合内容各部分都说到。 */
export function contentSummary(meta: {
  content_kinds: string[];
  image_count: number;
  video_count: number;
  duration_ms: number | null;
}): string {
  const parts: string[] = [];
  const kinds = meta.content_kinds;
  if (kinds.includes("text")) parts.push("文字");
  if (meta.image_count > 0) parts.push(`${meta.image_count} 张图`);
  else if (kinds.includes("image")) parts.push("图片");
  if (kinds.includes("live_photo")) parts.push("实况图");
  if (meta.video_count > 1) parts.push(`${meta.video_count} 个视频`);
  else if (kinds.includes("video"))
    parts.push(meta.duration_ms ? `视频 ${formatDuration(meta.duration_ms)}` : "视频");
  if (kinds.includes("audio")) parts.push("音频");
  if (kinds.includes("article")) parts.push("文章");
  if (kinds.includes("live")) parts.push("直播");
  return parts.join(" + ");
}

/** 毫秒 → `m:ss` 或 `h:mm:ss`；未知或非法返回空串。 */
export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return "";
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const two = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${two(m)}:${two(s)}` : `${m}:${two(s)}`;
}

/** 计数 → "1.2万" / "3.4亿"；站点没给的数字显示为 "—"，不当作 0。 */
export function formatCount(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "—";
  if (n < 10000) return String(n);
  if (n < 100000000) return `${(n / 10000).toFixed(n < 100000 ? 1 : 0).replace(/\.0$/, "")}万`;
  return `${(n / 100000000).toFixed(1).replace(/\.0$/, "")}亿`;
}

export const SORT_OPTIONS = [
  { label: "按发布时间", value: "" },
  { label: "按播放量", value: "plays" },
  { label: "按点赞数", value: "likes" },
  { label: "按评论数", value: "comments" },
  { label: "按收藏数", value: "favorites" },
  { label: "按时长", value: "duration" },
];

export const CONTENT_KIND_OPTIONS = [
  { label: "全部形式", value: "" },
  ...["text", "image", "live_photo", "video", "audio", "article", "live"].map((value) => ({
    label: contentKindLabel(value),
    value,
  })),
];

export const SAVE_STATE_OPTIONS = [
  { label: "不限", value: "" },
  { label: "完整", value: "complete" },
  { label: "部分", value: "partial" },
  { label: "待保存", value: "pending" },
  { label: "未下载（按订阅设置）", value: "deferred" },
  { label: "未知", value: "unknown" },
];

export const DELIVERY_STATE_LABELS: Record<string, string> = {
  running: "进行中",
  finishing: "收尾中",
  done: "已结束",
  cancelled: "已取消",
};

export const DELIVERY_ITEM_LABELS: Record<string, string> = {
  queued: "排队中",
  ready: "已在资源库",
  exported: "已写入下载文件夹",
  partial: "部分完成",
  failed: "失败",
  cancelled: "已取消",
};

export const SYNC_STATE_LABELS: Record<string, string> = {
  queued: "已加入下载队列",
  already_queued: "已在下载队列中",
  nothing: "没有需要下载的视频",
  failed: "未能加入下载",
};

export const COPY_STATE_LABELS: Record<string, string> = {
  present: "完好",
  changed: "已在 TRACE 之外被修改",
  missing: "文件已不在原处",
  writing: "正在写入",
  untracked: "TRACE 没有记录",
  saved: "已保存",
  deferred: "未下载（按订阅设置）",
  complete: "已保存",
  partial: "部分保存",
  pending: "待保存",
  unknown: "保存情况未知",
};

export function copyStateLabel(state: string): string {
  return COPY_STATE_LABELS[state] ?? state;
}

export function entryKindLabel(kind: string): string {
  return (
    (
      {
        folder: "文件夹",
        video: "视频",
        image: "图片",
        audio: "音频",
        text: "文字",
        article: "文章",
        live: "直播",
        other: "其他",
      } as Record<string, string>
    )[kind] ?? kind
  );
}
