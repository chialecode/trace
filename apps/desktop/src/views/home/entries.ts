import { authLabel } from "../../lib/acquisition";
import type { TimelineItemDto } from "../../lib/api";
import { mediaUrl } from "../../lib/api";
import { formatTime, platformOf, qualityLabel, saveProgressLabel } from "../../lib/format";
import { displayText } from "../../lib/safe";
import { contentSummary, formatCount, formatDuration, siteLabel } from "../../lib/sites";
import type { ContentKind, FeedEntry } from "../../lib/preview";

export type LiveEntry = FeedEntry & { publishedAt: string | null };

/** Start of the day after `date` (local time): the exclusive upper bound of "before this date". */
export function localDayEnd(date: string) {
  const day = new Date(date + "T00:00:00");
  day.setDate(day.getDate() + 1);
  return day.toISOString();
}

const FETCHABLE = new Set(["deferred", "partial", "pending", "failed"]);

function statsLine(item: TimelineItemDto): string | undefined {
  const s = item.meta?.stats;
  if (!s) return undefined;
  const parts = (
    [
      ["播放", s.plays],
      ["点赞", s.likes],
      ["评论", s.comments],
      ["收藏", s.favorites],
    ] as [string, number | null][]
  )
    .filter(([, value]) => value !== null)
    .map(([label, value]) => `${label} ${formatCount(value)}`);
  return parts.length ? parts.join(" · ") : undefined;
}

export function mapEntry(item: TimelineItemDto): LiveEntry {
  const meta = item.meta;
  const where = meta
    ? [meta.ip_region && `IP 属地 ${meta.ip_region}`, meta.location_name].filter(Boolean)
    : [];
  return {
    id: item.entry_id,
    platform: item.namespace.includes("bilibili") ? "bilibili" : item.namespace,
    platformLabel: meta ? siteLabel(meta.site) : platformOf(item.namespace),
    kind: (item.kind === "dynamic"
      ? "activity"
      : item.kind === "opus"
        ? "image"
        : item.kind) as ContentKind,
    title: item.title ?? "",
    body: "",
    time: formatTime(item.published_at),
    day: item.published_at?.slice(0, 10) ?? "发布时间未知",
    tags: (meta?.tags ?? []).slice(0, 6).map(displayText),
    art: "unavailable",
    duration: meta?.duration_ms ? formatDuration(meta.duration_ms) : undefined,
    contentLine: meta ? contentSummary(meta) || undefined : undefined,
    statsLine: statsLine(item),
    whereLine: where.length ? where.map((w) => displayText(String(w))).join(" · ") : undefined,
    canFetch: FETCHABLE.has(item.save_progress),
    saveLabel: saveProgressLabel(item.save_progress),
    qualityLabel: qualityLabel(item.quality_state),
    coverUrl: mediaUrl(item.cover_blob_id),
    sourceUrl: item.source_url,
    sourceKey: item.source_key ?? undefined,
    creatorName: item.creator_name,
    accountName: item.account_name ?? item.native_account_key,
    precision: item.published_precision ?? undefined,
    publishedAt: item.published_at,
    authenticationLabel: authLabel(item.authentication?.state),
  };
}
