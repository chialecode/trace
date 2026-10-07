/**
 * 展示格式化与状态文案（纯函数，Vitest 覆盖）。
 * 状态文案遵循 docs/design-rules/DESIGN.md 的正交状态用语：
 * 可访问性 / 保存进度 / 质量满足 分开陈述，不合并成单一"完成"。
 */

export const JOB_STATE_LABELS: Record<string, string> = {
  queued: "排队中",
  leased: "已领取",
  running: "执行中",
  waiting_retry: "等待重试",
  waiting_auth: "等待登录",
  waiting_plugin: "等待模块",
  waiting_storage: "等待存储",
  waiting_user: "等待用户处理",
  paused: "已暂停",
  succeeded: "已成功",
  failed: "已失败",
  cancelled: "已取消",
};

export const ACCESS_STATE_LABELS: Record<string, string> = {
  available: "可访问",
  unavailable: "不可访问",
  unknown: "未知",
  confirmed_deleted: "已删除（确认）",
};

export const SAVE_PROGRESS_LABELS: Record<string, string> = {
  unknown: "保存情况未知",
  pending: "未开始",
  partial: "部分保存",
  complete: "已保存",
  failed: "保存失败",
};

export const QUALITY_LABELS: Record<string, string> = {
  satisfied: "质量满足",
  degraded: "能力差距",
  unknown: "质量未知",
};

export function jobStateLabel(state: string): string {
  return JOB_STATE_LABELS[state] ?? state;
}

export function saveProgressLabel(state: string | null | undefined): string {
  if (!state) return "未开始";
  return SAVE_PROGRESS_LABELS[state] ?? state;
}

export function accessStateLabel(state: string | null | undefined): string {
  if (!state) return "未知";
  return ACCESS_STATE_LABELS[state] ?? state;
}

export function qualityLabel(state: string | null | undefined): string {
  if (!state) return "质量未知";
  return QUALITY_LABELS[state] ?? state;
}

/** naive-ui tag 类型映射（仅视觉，不改变状态语义）。 */
export function jobStateTagType(state: string): "success" | "info" | "warning" | "error" | "default" {
  switch (state) {
    case "succeeded":
      return "success";
    case "running":
    case "leased":
      return "info";
    case "queued":
    case "waiting_retry":
    case "paused":
      return "warning";
    case "waiting_auth":
    case "waiting_plugin":
    case "waiting_storage":
    case "waiting_user":
      return "warning";
    case "failed":
      return "error";
    case "cancelled":
      return "default";
    default:
      return "default";
  }
}

/** 字节数人性化（1024 基）；负数/NaN 原样返回"-"。 */
export function formatBytes(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n) || n < 0) return "-";
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let v = n;
  let i = -1;
  do {
    v /= 1024;
    i += 1;
  } while (v >= 1024 && i < units.length - 1);
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/** RFC3339 → 本地时间显示；非法输入原样返回。 */
export function formatTime(rfc3339: string | null | undefined): string {
  if (!rfc3339) return "-";
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return rfc3339;
  return d.toLocaleString("zh-CN", { hour12: false });
}

/** 平台命名空间 → 展示名（namespace 形如 "bilibili" 或 "bilibili:instance"）。 */
export function platformOf(namespace: string): string {
  return namespace.split(":")[0] ?? namespace;
}

/** 媒体保存摘要 → 一句话（区分"无声"与"失败"由证据字段决定）。 */
export function mediaSummaryLabel(summary: Record<string, unknown> | null | undefined): string {
  if (!summary) return "无媒体记录";
  const total = Number(summary["total"] ?? 0);
  const saved = Number(summary["saved"] ?? 0);
  if (total === 0) return "未发现媒体";
  if (saved >= total) return `已保存 ${saved}/${total}`;
  if (saved > 0) return `部分保存 ${saved}/${total}`;
  return "尚未保存";
}

export function timePrecisionLabel(value: string | null | undefined): string {
  return ({second:'精确到秒',day:'精确到日期',minute:'精确到分钟',millisecond:'精确到毫秒',discovery:'仅发现时间',unknown:'未知'} as Record<string,string>)[value ?? 'unknown'] ?? '未知';
}
export function mediaRoleLabel(role: string): string {
  return ({video:'视频',audio:'音频',video_segment:'视频分段',cover:'封面',image:'图片'} as Record<string,string>)[role] ?? '媒体资源';
}
