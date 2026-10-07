import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";

// mock @tauri-apps/api/core：验证参数名转换与空值处理。
const invoke = vi.hoisted(() => vi.fn(async (cmd: string, args?: unknown) => ({ cmd, args })));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));

describe("api 封装", () => {
  beforeEach(() => {
    invoke.mockClear();
  });

  it("可选参数显式置 null（对齐 Rust Option）", async () => {
    await api.accountList();
    expect(invoke).toHaveBeenCalledWith("account_list", { creatorId: null });
    await api.accountList("cre-1");
    expect(invoke).toHaveBeenCalledWith("account_list", { creatorId: "cre-1" });
  });

  it("凭证导入传递 secret 而不落地", async () => {
    await api.credentialImport("bilibili", null, "SESSDATA=xxx");
    expect(invoke).toHaveBeenCalledWith("credential_import", {
      platform: "bilibili",
      subject: null,
      secret: "SESSDATA=xxx",
    });
  });

  it("订阅范围映射", async () => {
    const r = (await api.subscriptionCreate(
      "acc-1",
      "recent_days",
      30,
      3600,
    )) as unknown as { args: Record<string, unknown> };
    expect(r.args.accountId).toBe("acc-1");
    expect(r.args.rangeDays).toBe(30);
    expect(r.args.checkIntervalSeconds).toBe(3600);
  });

  it("任务列表过滤器可空", async () => {
    await api.jobList();
    expect(invoke).toHaveBeenCalledWith("job_list", {
      stateFilter: null,
      jobType: null,
      limit: 100,
    });
  });
});

import timelinePage from '../../../../../crates/contracts/tests/golden/timeline_page.json';
import type { Page, TimelineItemDto } from '../api';
it('U04 shared Rust/TypeScript timeline sample retains counts, IDs and unknowns', () => {
  const page: Page<TimelineItemDto> = timelinePage;
  expect(page.items[0]?.entry_id).toBe('entry-9007199254740993');
  expect(page.items[0]?.published_at).toBeNull();
  expect(page.items[0]?.quality_state).toBe('unknown');
  expect(page.counts?.global).toBe(65);
  expect(page.counts?.filtered).toBe(1);
});
