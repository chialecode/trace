import { describe, expect, it } from "vitest";
import { displayText, hasHiddenCharacters, leafName } from "../safe";
import {
  contentSummary,
  copyStateLabel,
  entryLabel,
  formatCount,
  formatDuration,
  loginLabel,
  networkLabel,
  ruleStatusLabel,
  ruleStatusTone,
  SITES,
  siteLabel,
} from "../sites";

describe("site and rule labels", () => {
  it("names all eight sites in Chinese or their own name, and never invents one", () => {
    expect(SITES.map((s) => s.id)).toEqual([
      "bilibili",
      "douyin",
      "weibo",
      "xiaohongshu",
      "youtube",
      "x",
      "instagram",
      "tiktok",
    ]);
    expect(siteLabel("weibo")).toBe("微博");
    expect(siteLabel("bilibili:main")).toBe("哔哩哔哩");
    expect(siteLabel("unknownsite")).toBe("unknownsite");
    expect(siteLabel(null)).toBe("未知站点");
  });

  it("states what a rule can do without turning partial or missing work into done", () => {
    expect(ruleStatusLabel("implemented")).toBe("已实现");
    expect(ruleStatusLabel("partial")).toBe("部分实现");
    expect(ruleStatusLabel("unimplemented")).toBe("未实现");
    expect(ruleStatusTone("partial")).toBe("warning");
    expect(ruleStatusTone("unimplemented")).toBe("error");
    expect(loginLabel("required")).toBe("需要登录");
    expect(networkLabel("proxy_recommended")).toBe("建议使用代理");
    expect(entryLabel("preview")).toBe("预览勾选");
    expect(copyStateLabel("deferred")).toBe("未下载（按订阅设置）");
    expect(copyStateLabel("changed")).toContain("修改");
  });
});

describe("content and counters", () => {
  it("describes mixed content by every part it holds", () => {
    expect(
      contentSummary({
        content_kinds: ["text", "image", "video"],
        image_count: 3,
        video_count: 1,
        duration_ms: 90_000,
      }),
    ).toBe("文字 + 3 张图 + 视频 1:30");
    expect(
      contentSummary({
        content_kinds: ["video"],
        image_count: 0,
        video_count: 2,
        duration_ms: null,
      }),
    ).toBe("2 个视频");
    expect(
      contentSummary({
        content_kinds: ["live_photo"],
        image_count: 0,
        video_count: 0,
        duration_ms: null,
      }),
    ).toBe("实况图");
  });

  it("formats durations and counters, and shows an unreported counter as unknown", () => {
    expect(formatDuration(5_000)).toBe("0:05");
    expect(formatDuration(3_725_000)).toBe("1:02:05");
    expect(formatDuration(null)).toBe("");
    expect(formatDuration(-1)).toBe("");
    expect(formatCount(0)).toBe("0");
    expect(formatCount(9_999)).toBe("9999");
    expect(formatCount(12_345)).toBe("1.2万");
    expect(formatCount(250_000)).toBe("25万");
    expect(formatCount(130_000_000)).toBe("1.3亿");
    expect(formatCount(null)).toBe("—");
    expect(formatCount(Number.NaN)).toBe("—");
  });
});

describe("text from outside", () => {
  it("replaces controls and direction overrides so a name cannot hide its real end", () => {
    const rlo = String.fromCharCode(0x202e);
    const name = `invoice${rlo}fdp.exe`;
    expect(displayText(name)).toBe("invoice�fdp.exe");
    expect(hasHiddenCharacters(name)).toBe(true);
    expect(displayText("a\u0000b\u001fc\u007fd")).toBe("a�b�c�d");
    expect(displayText("普通的中文 name.mp4")).toBe("普通的中文 name.mp4");
    expect(hasHiddenCharacters("普通的中文 name.mp4")).toBe(false);
    expect(displayText(null)).toBe("");
  });

  it("takes the last segment of either separator", () => {
    expect(leafName("a\\b\\c.mp4")).toBe("c.mp4");
    expect(leafName("a/b/c.mp4")).toBe("c.mp4");
    expect(leafName("")).toBe("");
  });
});
