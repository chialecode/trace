<script setup lang="ts">
import { computed } from "vue";
import type { EntryMetaDto } from "../lib/api";
import { formatTime } from "../lib/format";
import { displayText } from "../lib/safe";
import { contentSummary, formatCount, siteLabel } from "../lib/sites";

const props = defineProps<{ meta: EntryMetaDto }>();

const RELATION: Record<string, string> = { original: "原创", repost: "转发", quote: "引用" };
const VISIBILITY: Record<string, string> = {
  public: "公开",
  followers: "仅粉丝可见",
  private: "仅自己可见",
  unlisted: "不公开列出",
};
/** Only counters the site actually reported; a missing counter is never shown as zero. */
const stats = computed(() => {
  const s = props.meta.stats;
  return (
    [
      ["播放", s.plays],
      ["点赞", s.likes],
      ["评论", s.comments],
      ["转发", s.shares],
      ["收藏", s.favorites],
      ["投币", s.coins],
      ["弹幕", s.danmaku],
    ] as [string, number | null][]
  )
    .filter(([, v]) => v !== null)
    .map(([k, v]) => `${k} ${formatCount(v)}`);
});
const where = computed(() =>
  [
    props.meta.ip_region && `IP 属地 ${props.meta.ip_region}`,
    props.meta.location_name && `位置 ${props.meta.location_name}`,
    props.meta.client && `来自 ${props.meta.client}`,
  ].filter(Boolean),
);
const flags = computed(() =>
  [
    props.meta.pinned && "置顶",
    props.meta.age_restricted && "年龄限制",
    props.meta.sponsored && "含广告或赞助",
    props.meta.visibility && (VISIBILITY[props.meta.visibility] ?? props.meta.visibility),
  ].filter(Boolean),
);
const content = computed(() =>
  contentSummary({
    content_kinds: props.meta.content_kinds,
    image_count: props.meta.image_count,
    video_count: props.meta.video_count,
    duration_ms: props.meta.duration_ms,
  }),
);
</script>

<template>
  <section class="detail-section entry-meta" aria-label="统一元数据">
    <h3>内容信息</h3>
    <dl>
      <dt>来源</dt>
      <dd>
        {{ siteLabel(meta.site) }} · {{ RELATION[meta.relation] ?? meta.relation }}
        <template v-if="meta.related_key">（关联 {{ displayText(meta.related_key) }}）</template>
      </dd>
      <template v-if="content">
        <dt>内容</dt>
        <dd>{{ content }}</dd>
      </template>
      <template v-if="stats.length">
        <dt>数据</dt>
        <dd>
          {{ stats.join(" · ") }}
          <small v-if="meta.stats.observed_at"
            >（{{ formatTime(meta.stats.observed_at) }} 观察到）</small
          >
        </dd>
      </template>
      <template v-if="where.length">
        <dt>来自</dt>
        <dd>{{ where.map((w) => displayText(String(w))).join(" · ") }}</dd>
      </template>
      <template v-if="meta.collection || meta.category || meta.language">
        <dt>归类</dt>
        <dd>
          {{
            [meta.collection && `合集 ${meta.collection}`, meta.category, meta.language]
              .filter(Boolean)
              .map((x) => displayText(String(x)))
              .join(" · ")
          }}
        </dd>
      </template>
      <template v-if="flags.length">
        <dt>标记</dt>
        <dd>{{ flags.join(" · ") }}</dd>
      </template>
      <template v-if="meta.edited_at">
        <dt>编辑于</dt>
        <dd>{{ formatTime(meta.edited_at) }}</dd>
      </template>
      <template v-if="meta.mentions.length">
        <dt>提到</dt>
        <dd>{{ meta.mentions.map((m) => "@" + displayText(m)).join(" ") }}</dd>
      </template>
      <template v-if="meta.canonical_url">
        <dt>原链接</dt>
        <dd class="url">{{ displayText(meta.canonical_url) }}</dd>
      </template>
    </dl>
  </section>
</template>

<style scoped>
dl {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px 12px;
  margin: 0;
  font-size: 0.8125rem;
}
dt {
  color: #66748a;
}
dd {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
}
.url {
  font-family: ui-monospace, monospace;
  font-size: 0.75rem;
}
</style>
