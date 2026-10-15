import type { MediaSpecDto } from "./api";
export function authLabel(state?: string) {
  return (
    (
      {
        authenticated: "登录获取",
        anonymous: "未登录获取",
        expired: "认证已失效",
        unknown: "身份未知",
        mixed: "混合获取身份",
      } as Record<string, string>
    )[state ?? "unknown"] ?? "身份未知"
  );
}
export function actualSpec(spec?: MediaSpecDto | null) {
  return `${spec?.width ?? "未知"} × ${spec?.height ?? "未知"} · ${spec?.fps ?? "未知"} fps · ${(spec?.bitrate_bps ?? spec?.bit_rate) ? ((spec?.bitrate_bps ?? spec?.bit_rate ?? 0) / 1000).toFixed(0) + " kbps" : "码率未知"} · ${spec?.codec ?? "编码未知"}`;
}
