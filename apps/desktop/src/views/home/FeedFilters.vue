<script setup lang="ts">
import { computed } from "vue";
import TraceIcon from "../../components/TraceIcon.vue";
import { useWorkspaceStore } from "../../stores/workspace";
import type { AuthenticationState } from "../../lib/api";
import { CONTENT_KIND_OPTIONS, SAVE_STATE_OPTIONS, SORT_OPTIONS } from "../../lib/sites";

const props = defineProps<{
  counts: { global: number | null; filtered: number | null };
  /** Entries currently loaded after filtering. */
  shown: number;
  /** Frequent tags offered as suggestions in the tag filter. */
  tagOptions?: string[];
}>();
const kind = defineModel<string>("kind", { required: true });
const oldest = defineModel<boolean>("oldest", { required: true });
const authentication = defineModel<AuthenticationState | "">("authentication", { required: true });
const fromDate = defineModel<string>("fromDate", { required: true });
const beforeDate = defineModel<string>("beforeDate", { required: true });
const minHeight = defineModel<string>("minHeight", { required: true });
const codec = defineModel<string>("codec", { required: true });
const saveState = defineModel<string>("saveState", { required: true });
const sort = defineModel<string>("sort", { default: "" });
const tag = defineModel<string>("tag", { default: "" });
const contentKind = defineModel<string>("contentKind", { default: "" });
const emit = defineEmits<{ reset: [] }>();
const workspace = useWorkspaceStore();

const allTabs = [
  { label: "全部", value: "", icon: "bell" },
  { label: "动态", value: "activity", icon: "activity" },
  { label: "视频", value: "video", icon: "play" },
  { label: "直播", value: "live", icon: "video" },
  { label: "图片", value: "image", icon: "image" },
  { label: "文章", value: "article", icon: "file" },
  { label: "合集", value: "collection", icon: "collection" },
];
const tabs = computed(() =>
  workspace.preview
    ? allTabs
    : allTabs
        .filter((t) => t.value !== "collection")
        .map((t) => (t.value === "activity" ? { ...t, value: "dynamic" } : t)),
);
const stats = computed(() =>
  workspace.preview
    ? [
        { label: "全部内容", value: "1,284", icon: "file", kind: "" },
        { label: "视频", value: "428", icon: "play", kind: "video" },
        { label: "图文", value: "536", icon: "image", kind: "image" },
        { label: "直播", value: "142", icon: "video", kind: "live" },
        { label: "合集", value: "178", icon: "collection", kind: "collection" },
      ]
    : [
        { label: "全档案内容", value: String(props.counts.global ?? "—"), icon: "file", kind: "" },
        {
          label: "符合筛选",
          value: String(props.counts.filtered ?? "—"),
          icon: "play",
          kind: kind.value,
        },
      ],
);
</script>

<template>
  <div class="content-tabs" aria-label="内容类型">
    <button
      v-for="tab in tabs"
      :key="tab.value"
      :class="{ active: kind === tab.value }"
      :aria-pressed="kind === tab.value"
      @click="kind = tab.value"
    >
      <TraceIcon :name="tab.icon" :size="15" />{{ tab.label }}
    </button>
    <label v-if="!workspace.preview" class="sort-control"
      ><span class="sr-only">排序依据</span
      ><select v-model="sort" aria-label="排序依据">
        <option v-for="o in SORT_OPTIONS" :key="o.value" :value="o.value">{{ o.label }}</option>
      </select></label
    >
    <label class="sort-control"
      ><span class="sr-only">时间排序</span
      ><select v-model="oldest" aria-label="时间排序">
        <option :value="false">{{ sort ? "从高到低" : "按时间排序" }}</option>
        <option :value="true">{{ sort ? "从低到高" : "最早发布优先" }}</option>
      </select></label
    >
  </div>
  <label v-if="!workspace.preview" class="authentication-filter"
    >获取身份
    <select v-model="authentication" aria-label="获取身份">
      <option value="">全部获取历史</option>
      <option value="authenticated">登录时间线</option>
      <option value="anonymous">未登录时间线</option>
      <option value="expired">认证已失效</option>
      <option value="unknown">身份未知</option>
    </select></label
  >
  <details v-if="!workspace.preview" class="archive-filters">
    <summary>日期、规格与保存状态</summary>
    <div>
      <label>起始日期 <input v-model="fromDate" type="date" aria-label="起始日期" /></label>
      <label>结束日期 <input v-model="beforeDate" type="date" aria-label="结束日期" /></label>
      <label
        >最低高度
        <select v-model="minHeight" aria-label="最低高度">
          <option value="">不限</option>
          <option value="720">720p</option>
          <option value="1080">1080p</option>
          <option value="2160">2160p</option>
        </select></label
      >
      <label
        >视频编码
        <select v-model="codec" aria-label="视频编码">
          <option value="">不限</option>
          <option value="h264">H.264</option>
          <option value="hevc">HEVC</option>
          <option value="av1">AV1</option>
        </select></label
      >
      <label
        >保存状态
        <select v-model="saveState" aria-label="保存状态">
          <option v-for="o in SAVE_STATE_OPTIONS" :key="o.value" :value="o.value">
            {{ o.label }}
          </option>
        </select></label
      >
      <label
        >内容形式
        <select v-model="contentKind" aria-label="内容形式">
          <option v-for="o in CONTENT_KIND_OPTIONS" :key="o.value" :value="o.value">
            {{ o.label }}
          </option>
        </select></label
      >
      <label
        >话题标签
        <input
          v-model="tag"
          list="feed-tag-options"
          autocomplete="off"
          placeholder="不含 #"
          aria-label="话题标签"
        />
        <datalist id="feed-tag-options">
          <option v-for="t in tagOptions ?? []" :key="t" :value="t" />
        </datalist>
      </label>
      <button @click="emit('reset')">清除筛选</button
      ><small
        >按当前正文版本已验证媒体的实际规格筛选；日期使用本地时区，未知发布时间不匹配日期范围。</small
      >
    </div>
  </details>
  <div class="stats-bar" :aria-label="workspace.preview ? '示例统计' : '全档案与筛选统计'">
    <button v-for="stat in stats" :key="stat.label" @click="kind = stat.kind">
      <TraceIcon :name="stat.icon" :size="17" /><span
        >{{ stat.label }}<strong>{{ stat.value }}</strong></span
      >
    </button>
  </div>
  <div v-if="workspace.query || workspace.platform" class="filter-summary">
    <span>{{ workspace.preview ? "示例筛选" : "当前已加载" }} · {{ shown }} 条内容</span
    ><button @click="emit('reset')">清除筛选</button>
  </div>
</template>
