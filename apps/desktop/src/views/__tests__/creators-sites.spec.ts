import { mount, flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent } from "vue";
import { NMessageProvider } from "naive-ui";
import CreatorsView from "../CreatorsView.vue";
import { api, type AccountDto, type CreatorDto, type Recognition } from "../../lib/api";
import { partial } from "../../test/fixtures";

vi.mock("../../lib/api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../lib/api")>();
  const names = [
    "creatorList",
    "accountList",
    "accountResolve",
    "accountBind",
    "subscriptionList",
    "subscriptionCreate",
    "subscriptionUpdate",
    "credentialList",
    "siteRecognize",
  ];
  return {
    ...original,
    api: { ...original.api, ...Object.fromEntries(names.map((n) => [n, vi.fn()])) },
  };
});

const creator = partial<CreatorDto>({
  creator_id: "c1",
  display_name: "合成创作者",
  status: "active",
  notes: null,
  aliases: [],
  tags: [],
});
const account = partial<AccountDto>({
  account_id: "a1",
  creator_id: "c1",
  namespace: "weibo",
  native_account_key: "10001",
  display_name: "合成账号",
  profile_url: null,
});
let wrapper: VueWrapper;

async function open() {
  const host = defineComponent({
    components: { CreatorsView, NMessageProvider },
    template: "<NMessageProvider><CreatorsView/></NMessageProvider>",
  });
  wrapper = mount(host, { attachTo: document.body });
  await flushPromises();
}
const button = (label: string) =>
  [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
async function press(label: string) {
  expect(button(label), label).toBeTruthy();
  button(label)!.click();
  await flushPromises();
}
async function type(input: HTMLInputElement | undefined, value: string) {
  expect(input).toBeTruthy();
  input!.value = value;
  input!.dispatchEvent(new Event("input"));
  await flushPromises();
}
const modalInputs = () => [...document.body.querySelectorAll<HTMLInputElement>(".n-modal input")];

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.creatorList).mockResolvedValue({ items: [creator], next_cursor: null });
  vi.mocked(api.accountList).mockResolvedValue({ items: [account], next_cursor: null });
  vi.mocked(api.subscriptionList).mockResolvedValue({ items: [], next_cursor: null });
  vi.mocked(api.credentialList).mockResolvedValue({ items: [], next_cursor: null });
});
afterEach(() => {
  wrapper.unmount();
  document.body.innerHTML = "";
  vi.useRealTimers();
});

describe("linking one creator to accounts on several sites", () => {
  const found = (over: Record<string, unknown> = {}) =>
    partial<Recognition>({
      shown: "weibo.test/u/10001",
      site: "weibo",
      site_title: "微博",
      object: "account",
      rules: [],
      needs_expansion: false,
      message: null,
      ...over,
    });

  it("lists all eight sites to bind to", async () => {
    await open();
    await press("绑定平台账号");
    const text = document.body.textContent ?? "";
    expect(text).toContain("绑定平台账号");
    // The select options render lazily; the component's own list is the source of truth.
    const { SITES } = await import("../../lib/sites");
    expect(SITES).toHaveLength(8);
  });

  it("recognizes a pasted profile link, picks its site, resolves it and binds the account", async () => {
    vi.mocked(api.siteRecognize).mockResolvedValue(found());
    vi.mocked(api.accountResolve).mockResolvedValue({
      account_id: "x",
      native_account_key: "10001",
      display_name: "合成账号",
    });
    vi.mocked(api.accountBind).mockResolvedValue({ account_id: "a1" });
    await open();
    await press("绑定平台账号");
    vi.useFakeTimers();
    await type(modalInputs()[0], "https://weibo.test/u/10001");
    await vi.advanceTimersByTimeAsync(500);
    vi.useRealTimers();
    await flushPromises();
    expect(api.siteRecognize).toHaveBeenCalledWith("https://weibo.test/u/10001");
    expect(document.body.textContent).toContain("已识别：微博 · 账号主页");
    await press("解析并绑定");
    expect(api.accountResolve).toHaveBeenCalledWith(
      "weibo",
      "https://weibo.test/u/10001",
      undefined,
    );
    expect(api.accountBind).toHaveBeenCalledWith("c1", "weibo", "10001");
  });

  it("warns that a single-content link is not a profile, and says so for unsupported links", async () => {
    vi.mocked(api.siteRecognize).mockResolvedValueOnce(found({ object: "entry" }));
    await open();
    await press("绑定平台账号");
    vi.useFakeTimers();
    await type(modalInputs()[0], "https://weibo.test/1/P1");
    await vi.advanceTimersByTimeAsync(500);
    vi.useRealTimers();
    await flushPromises();
    expect(document.body.textContent).toContain("不是作者主页");
    vi.mocked(api.siteRecognize).mockResolvedValueOnce(
      found({ site: null, site_title: null, object: null, message: null }),
    );
    vi.useFakeTimers();
    await type(modalInputs()[0], "https://nowhere.test/u/1");
    await vi.advanceTimersByTimeAsync(500);
    vi.useRealTimers();
    await flushPromises();
    expect(document.body.textContent).toContain("不属于已支持的网站");
  });

  it("does not ask the daemon about plain account ids, and drops an answer that arrives late", async () => {
    await open();
    await press("绑定平台账号");
    vi.useFakeTimers();
    await type(modalInputs()[0], "12345");
    await vi.advanceTimersByTimeAsync(600);
    expect(api.siteRecognize).not.toHaveBeenCalled();
    let late!: (v: Recognition) => void;
    vi.mocked(api.siteRecognize).mockReturnValueOnce(
      new Promise((resolve) => {
        late = resolve;
      }),
    );
    await type(modalInputs()[0], "https://weibo.test/u/1");
    await vi.advanceTimersByTimeAsync(500);
    await type(modalInputs()[0], "99999");
    vi.useRealTimers();
    late(found());
    await flushPromises();
    expect(document.body.textContent).not.toContain("已识别");
  });
});

describe("subscription auto download", () => {
  async function openSubscription(existing?: Record<string, unknown>) {
    if (existing)
      vi.mocked(api.subscriptionList).mockResolvedValue({
        items: [
          partial({
            subscription_id: "s1",
            account_id: "a1",
            content_type: "video",
            range: { kind: "from_now" },
            state: "enabled",
            check_interval_seconds: 300,
            quality: {},
            capture_chat: false,
            capture_subtitles: false,
            credential_id: null,
            coverage: [],
            ...existing,
          }),
        ],
        next_cursor: null,
      });
    await open();
    await press("账号/订阅");
    if (existing) {
      [...document.body.querySelectorAll<HTMLElement>(".n-tabs-tab")]
        .find((t) => t.textContent?.trim() === "订阅")!
        .click();
      await flushPromises();
    }
  }

  it("is off for a new subscription and explains what that means", async () => {
    vi.mocked(api.subscriptionCreate).mockResolvedValue({ subscription_id: "s2" });
    await openSubscription();
    await press("创建订阅");
    const text = document.body.textContent ?? "";
    expect(text).toContain("自动下载新发现的视频");
    expect(text).toContain("默认关闭");
    expect(text).toContain("未下载（按订阅设置）");
    await press("保存订阅");
    const options = vi.mocked(api.subscriptionCreate).mock.calls[0]![5]!;
    expect(options.autoDownload).toBeUndefined();
  });

  it("turns on for a new subscription when ticked", async () => {
    vi.mocked(api.subscriptionCreate).mockResolvedValue({ subscription_id: "s2" });
    await openSubscription();
    await press("创建订阅");
    const box = document.body.querySelector<HTMLInputElement>(".auto-download input")!;
    box.click();
    await flushPromises();
    expect(document.body.textContent).toContain("之后检查到的新视频会自动下载");
    await press("保存订阅");
    expect(vi.mocked(api.subscriptionCreate).mock.calls[0]![5]).toMatchObject({
      autoDownload: true,
    });
  });

  it("changes an existing subscription only when the choice changed", async () => {
    vi.mocked(api.subscriptionUpdate).mockResolvedValue({});
    await openSubscription({ auto_download: true });
    await press("配置 / 覆盖");
    const box = document.body.querySelector<HTMLInputElement>(".auto-download input")!;
    expect(box.checked).toBe(true);
    await press("保存订阅");
    expect(vi.mocked(api.subscriptionUpdate).mock.calls[0]![1]).not.toHaveProperty("auto_download");
    await press("配置 / 覆盖");
    document.body.querySelector<HTMLInputElement>(".auto-download input")!.click();
    await flushPromises();
    await press("保存订阅");
    expect(vi.mocked(api.subscriptionUpdate).mock.calls.at(-1)![1]).toMatchObject({
      auto_download: false,
    });
  });
  it("flips the setting from the subscription list without opening the dialog", async () => {
    vi.mocked(api.subscriptionUpdate).mockResolvedValue({});
    await openSubscription({ auto_download: false });
    await press("开启自动下载");
    expect(api.subscriptionUpdate).toHaveBeenCalledWith("s1", { auto_download: true });
  });
});
