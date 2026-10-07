import { describe, expect, it } from "vitest";
import {
  accessStateLabel,
  formatBytes,
  formatTime,
  jobStateLabel,
  jobStateTagType,
  mediaSummaryLabel,
  platformOf,
  qualityLabel,
  saveProgressLabel,
} from "../format";

describe("状态文案（正交状态分开陈述）", () => {
  it("任务状态映射为中文", () => {
    expect(jobStateLabel("queued")).toBe("排队中");
    expect(jobStateLabel("waiting_auth")).toBe("等待登录");
    expect(jobStateLabel("waiting_plugin")).toBe("等待模块");
    expect(jobStateLabel("succeeded")).toBe("已成功");
  });

  it("未知状态原样显示，不猜语义", () => {
    expect(jobStateLabel("mystery_state")).toBe("mystery_state");
  });

  it("可访问性/保存进度/质量互不合并", () => {
    expect(accessStateLabel("available")).toBe("可访问");
    expect(saveProgressLabel("partial")).toBe("部分保存");
    expect(qualityLabel("degraded")).toBe("能力差距");
    expect(saveProgressLabel(null)).toBe("未开始");
  });

  it("tag 类型：等待登录与失败视觉可区分", () => {
    expect(jobStateTagType("waiting_auth")).toBe("warning");
    expect(jobStateTagType("failed")).toBe("error");
    expect(jobStateTagType("succeeded")).toBe("success");
  });
});

describe("格式化", () => {
  it("字节数人性化", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(1024 * 1024)).toBe("1.0 MB");
    expect(formatBytes(NaN)).toBe("-");
    expect(formatBytes(-5)).toBe("-");
    expect(formatBytes(null)).toBe("-");
  });

  it("时间格式化：非法输入原样返回", () => {
    expect(formatTime(null)).toBe("-");
    expect(formatTime("not-a-time")).toBe("not-a-time");
    expect(formatTime("2026-09-26T12:00:00Z")).not.toBe("-");
  });

  it("平台命名空间取首段", () => {
    expect(platformOf("bilibili")).toBe("bilibili");
    expect(platformOf("fake:instance-a")).toBe("fake");
  });

  it("媒体摘要区分无声与未保存", () => {
    expect(mediaSummaryLabel(null)).toBe("无媒体记录");
    expect(mediaSummaryLabel({ total: 0, saved: 0 })).toBe("未发现媒体");
    expect(mediaSummaryLabel({ total: 2, saved: 2 })).toBe("已保存 2/2");
    expect(mediaSummaryLabel({ total: 2, saved: 1 })).toBe("部分保存 1/2");
  });
});
