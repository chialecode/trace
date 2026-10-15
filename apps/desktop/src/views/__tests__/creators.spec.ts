import { mount, flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent } from "vue";
import { NMessageProvider } from "naive-ui";
import CreatorsView from "../CreatorsView.vue";
import { api, type AccountDto, type CreatorDto } from "../../lib/api";
import { partial } from "../../test/fixtures";

vi.mock("../../lib/api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../lib/api")>();
  return {
    ...original,
    api: {
      ...original.api,
      creatorList: vi.fn(),
      creatorUpdate: vi.fn(),
      accountList: vi.fn(),
      subscriptionList: vi.fn(),
      subscriptionCreate: vi.fn(),
      credentialList: vi.fn(),
    },
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
  namespace: "bilibili",
  native_account_key: "1",
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
  const target = button(label);
  expect(target, label).toBeTruthy();
  target!.click();
  await flushPromises();
}

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
});

describe("creators view", () => {
  it("lists creators and saves edited aliases as a deduplicated list", async () => {
    await open();
    expect(document.body.textContent).toContain("合成创作者");
    await press("编辑");
    const inputs = [...document.body.querySelectorAll<HTMLInputElement>(".n-modal input")];
    expect(inputs[0]!.value).toBe("合成创作者");
    inputs[1]!.value = "甲，乙,甲";
    inputs[1]!.dispatchEvent(new Event("input"));
    await flushPromises();
    await press("保存");
    expect(api.creatorUpdate).toHaveBeenCalledWith("c1", "合成创作者", "", {
      aliases: ["甲", "乙"],
      tags: [],
    });
  });

  it("creates a subscription for an account listed under the creator", async () => {
    await open();
    await press("账号/订阅");
    expect(api.accountList).toHaveBeenCalledWith("c1");
    expect(api.subscriptionList).toHaveBeenCalledWith("a1");
    await press("创建订阅");
    await press("保存订阅");
    expect(api.subscriptionCreate).toHaveBeenCalledWith("a1", "from_now", undefined, 300, "video", {
      credentialId: undefined,
      captureChat: false,
      captureSubtitles: false,
      quality: { min_height: null, min_fps: null, strict: false, prefer_fps: false },
      start: undefined,
      end: undefined,
    });
  });
});
