import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";

// mock @tauri-apps/api/core：验证参数名转换与空值处理。
const invoke = vi.hoisted(() => vi.fn(async (cmd: string, args?: unknown) => ({ cmd, args })));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));

describe("api 封装", () => {
  beforeEach(() => {
    invoke.mockClear();
  });

  it('c08_public_search_forwards_shared_scope_and_continuation',async()=>{
    await api.searchQuery('中文','article',1,{creator_id:'c',namespace:'bilibili',from:'2026-01-01T00:00:00Z',to:'2026-02-01T00:00:00Z',cursor:'opaque'});
    expect(invoke).toHaveBeenLastCalledWith('search_query',{request:{query:'中文',kind:'article',limit:1,creator_id:'c',namespace:'bilibili',from:'2026-01-01T00:00:00Z',to:'2026-02-01T00:00:00Z',cursor:'opaque'}});
    await api.searchQuery('字');
    expect(invoke).toHaveBeenLastCalledWith('search_query',{request:{query:'字',kind:null,limit:50,creator_id:null,namespace:null,from:null,to:null,cursor:null}});
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
    )) as unknown as { args: { request: {account_id:string;range:{kind:string;days:number};check_interval_seconds:number;credential_id:string|null;capture_chat:boolean} } };
    // M1 forwards the shared Rust DTO. The same account, days and interval
    // assertions now address that DTO, including the previously dropped scope.
    expect(r.args.request.account_id).toBe("acc-1");
    expect(r.args.request.range.days).toBe(30);
    expect(r.args.request.check_interval_seconds).toBe(3600);
    expect(r.args.request.range.kind).toBe('recent_days');
    expect(r.args.request.credential_id).toBeNull();
    expect(r.args.request.capture_chat).toBe(false);
    await api.subscriptionCreate('acc-1','from_now',undefined,30,'live',{credentialId:'credential-bili',captureChat:true});
    expect(invoke).toHaveBeenLastCalledWith('subscription_create',{request:{account_id:'acc-1',content_type:'live',range:{kind:'from_now'},check_interval_seconds:30,credential_id:'credential-bili',capture_chat:true,capture_subtitles:false}});
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
