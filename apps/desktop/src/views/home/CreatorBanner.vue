<script setup lang="ts">
import TraceIcon from "../../components/TraceIcon.vue";
import ReferenceArt from "../../components/ReferenceArt.vue";
import { useWorkspaceStore } from "../../stores/workspace";
import { platformOf } from "../../lib/format";
import type { AccountDto, CreatorDto, SubscriptionDto } from "../../lib/api";

defineProps<{
  creator?: CreatorDto;
  creators: CreatorDto[];
  visibleAccounts: AccountDto[];
  subscription?: SubscriptionDto;
  /** Preview-only toggle; the live page reads `subscription` instead. */
  subscribed: boolean;
  savingSubscription: boolean;
  managementError: string;
}>();
const creatorId = defineModel<string>("creatorId", { required: true });
const accountId = defineModel<string>("accountId", { required: true });
const emit = defineEmits<{ retry: []; toggle: []; save: []; previewAction: [] }>();
const workspace = useWorkspaceStore();
</script>

<template>
  <section class="creator-banner">
    <div class="banner-art">
      <ReferenceArt name="banner" alt="" :local="workspace.preview" />
    </div>
    <div class="creator-avatar">
      <ReferenceArt
        :name="workspace.preview ? 'portrait' : 'unavailable'"
        :alt="workspace.preview ? '森中人头像（示例）' : '档案头像'"
        :local="workspace.preview"
      />
    </div>
    <div class="creator-info">
      <h1>
        {{ workspace.preview ? "森中人" : (creator?.display_name ?? "我的档案")
        }}<span v-if="workspace.preview" class="verified" aria-label="示例认证"
          ><TraceIcon name="check" :size="12"
        /></span>
      </h1>
      <p class="creator-handle">
        {{ workspace.preview ? "@morinaka_jin" : "记录、收集，留住每一份热爱。" }}
      </p>
      <p class="creator-bio">
        {{
          workspace.preview
            ? "分享动画、游戏与生活的随笔。"
            : creator?.notes || "从创作者出发，浏览已收录的内容。"
        }}
      </p>
      <div v-if="workspace.preview" class="creator-platforms">
        <span><b class="mini-mark bilibili">bili</b>Bilibili</span
        ><span><b class="mini-mark youtube">▶</b>YouTube</span><span>𝕏 <small>X +2</small></span>
      </div>
      <div v-else class="creator-picker">
        <label
          >创作者
          <select v-model="creatorId" aria-label="选择创作者">
            <option value="">全部创作者</option>
            <option v-for="c in creators" :key="c.creator_id" :value="c.creator_id">
              {{ c.display_name }}
            </option>
          </select></label
        ><label
          >来源账号
          <select v-model="accountId" aria-label="选择来源账号">
            <option value="">选择账号</option>
            <option v-for="a in visibleAccounts" :key="a.account_id" :value="a.account_id">
              {{ platformOf(a.namespace) }} · {{ a.display_name || a.native_account_key }}
            </option>
          </select></label
        >
        <p v-if="managementError" role="alert">
          {{ managementError }} <button @click="emit('retry')">重试</button>
        </p>
      </div>
      <router-link v-if="!workspace.preview" to="/creators" class="creator-manage"
        >管理创作者与订阅 <TraceIcon name="chevron" :size="13"
      /></router-link>
    </div>
    <div class="subscribe-actions">
      <button v-if="workspace.preview" class="primary-button" @click="emit('toggle')">
        <TraceIcon :name="subscribed ? 'bell' : 'plus'" :size="17" />{{
          subscribed ? "订阅中" : "订阅"
        }}
      </button>
      <button
        v-else-if="accountId"
        class="primary-button"
        :disabled="savingSubscription"
        @click="emit('save')"
      >
        {{
          savingSubscription
            ? "正在保存…"
            : subscription?.state === "enabled"
              ? "暂停订阅"
              : subscription
                ? "恢复订阅"
                : "订阅此账号"
        }}</button
      ><router-link v-else class="primary-button" to="/creators"
        ><TraceIcon name="plus" :size="16" />添加订阅</router-link
      >
      <button
        v-if="workspace.preview"
        class="banner-more icon-button"
        aria-label="预览说明"
        @click="emit('previewAction')"
      >
        <TraceIcon name="more" />
      </button>
    </div>
  </section>
</template>
