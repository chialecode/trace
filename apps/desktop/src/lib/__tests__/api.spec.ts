import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";

// mock @tauri-apps/api/core：所有 UI 请求经同一个 daemon_call 转发，验证命令名、参数名与空值处理。
const invoke = vi.hoisted(() => vi.fn(async (cmd: string, args?: unknown) => ({ cmd, args })));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));

const forwarded = (command: string, args: Record<string, unknown>) =>
  ["daemon_call", { command, args }] as const;

describe("api 封装", () => {
  beforeEach(() => {
    invoke.mockClear();
  });

  it("public_search_forwards_shared_scope_and_continuation", async () => {
    await api.searchQuery("中文", "article", 1, {
      creator_id: "c",
      namespace: "bilibili",
      from: "2026-01-01T00:00:00Z",
      to: "2026-02-01T00:00:00Z",
      cursor: "opaque",
    });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("search.query", {
        query: "中文",
        kind: "article",
        limit: 1,
        creator_id: "c",
        namespace: "bilibili",
        from: "2026-01-01T00:00:00Z",
        to: "2026-02-01T00:00:00Z",
        cursor: "opaque",
      }),
    );
    await api.searchQuery("字");
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("search.query", {
        query: "字",
        kind: null,
        limit: 50,
        creator_id: null,
        namespace: null,
        from: null,
        to: null,
        cursor: null,
      }),
    );
  });

  it("可选参数显式置 null（对齐 Rust Option）", async () => {
    await api.accountList();
    expect(invoke).toHaveBeenCalledWith(...forwarded("account.list", { creator_id: null }));
    await api.accountList("cre-1");
    expect(invoke).toHaveBeenCalledWith(...forwarded("account.list", { creator_id: "cre-1" }));
  });

  it("凭证导入传递 secret 而不落地", async () => {
    await api.credentialImport("bilibili", null, "SESSDATA=xxx");
    expect(invoke).toHaveBeenCalledWith(
      ...forwarded("credential.import", {
        platform: "bilibili",
        subject: null,
        secret: "SESSDATA=xxx",
      }),
    );
  });

  it("订阅范围映射", async () => {
    const r = (await api.subscriptionCreate("acc-1", "recent_days", 30, 3600)) as unknown as {
      args: {
        args: {
          account_id: string;
          range: { kind: string; days: number };
          check_interval_seconds: number;
          credential_id: string | null;
          capture_chat: boolean;
        };
      };
    };
    // The request carries the shared Rust DTO: account, days, interval and scope.
    expect(r.args.args.account_id).toBe("acc-1");
    expect(r.args.args.range.days).toBe(30);
    expect(r.args.args.check_interval_seconds).toBe(3600);
    expect(r.args.args.range.kind).toBe("recent_days");
    expect(r.args.args.credential_id).toBeNull();
    expect(r.args.args.capture_chat).toBe(false);
    await api.subscriptionCreate("acc-1", "from_now", undefined, 30, "live", {
      credentialId: "credential-bili",
      captureChat: true,
    });
    expect(invoke).toHaveBeenLastCalledWith(
      ...forwarded("subscription.create", {
        account_id: "acc-1",
        content_type: "live",
        range: { kind: "from_now" },
        check_interval_seconds: 30,
        credential_id: "credential-bili",
        capture_chat: true,
        capture_subtitles: false,
      }),
    );
  });

  it("任务列表过滤器可空", async () => {
    await api.jobList();
    expect(invoke).toHaveBeenCalledWith(
      ...forwarded("job.list", { state: null, job_type: null, limit: 100 }),
    );
  });
});

import timelinePage from "../../../../../crates/contracts/tests/golden/timeline_page.json";
import type { Page, TimelineItemDto } from "../api";
it("shared Rust/TypeScript timeline sample retains counts, IDs and unknowns", () => {
  const page = timelinePage as unknown as Page<TimelineItemDto>;
  expect(page.items[0]?.entry_id).toBe("entry-9007199254740993");
  expect(page.items[0]?.published_at).toBeNull();
  expect(page.items[0]?.quality_state).toBe("unknown");
  expect(page.counts?.global).toBe(65);
  expect(page.counts?.filtered).toBe(1);
});
