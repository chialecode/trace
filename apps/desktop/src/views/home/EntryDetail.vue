<script setup lang="ts">
import { NEmpty } from "naive-ui";
import TraceIcon from "../../components/TraceIcon.vue";
import ReferenceArt from "../../components/ReferenceArt.vue";
import MediaBrowser from "../../components/MediaBrowser.vue";
import ArchiveDocument from "../../components/ArchiveDocument.vue";
import LiveRecording from "../../components/LiveRecording.vue";
import ArchiveRemoval from "../../components/ArchiveRemoval.vue";
import QualityPanel from "../../components/QualityPanel.vue";
import ArchiveHistory from "../../components/ArchiveHistory.vue";
import AcquisitionHistory from "../../components/AcquisitionHistory.vue";
import EntryMetaPanel from "../../components/EntryMetaPanel.vue";
import { useWorkspaceStore } from "../../stores/workspace";
import { platformMark, type FeedEntry } from "../../lib/preview";
import { timePrecisionLabel } from "../../lib/format";
import type { EntryDetailState } from "./useEntryDetail";

defineProps<{
  selected: FeedEntry | null;
  selectedId: string | null;
  related: FeedEntry[];
  detail: EntryDetailState;
  selectionNotice: string;
}>();
const relatedTab = defineModel<boolean>("relatedTab", { required: true });
const emit = defineEmits<{
  refreshLive: [];
  moreMedia: [];
  openSource: [];
  recheck: [];
  fetch: [];
  pick: [entry: FeedEntry];
  previewAction: [];
}>();
const workspace = useWorkspaceStore();
</script>

<template>
  <aside class="detail-column" aria-label="内容详情">
    <div class="detail-tabs">
      <button
        :class="{ active: !relatedTab }"
        :aria-pressed="!relatedTab"
        @click="relatedTab = false"
      >
        <span class="detail-dot">◉</span>详情</button
      ><button
        :class="{ active: relatedTab }"
        :aria-pressed="relatedTab"
        @click="relatedTab = true"
      >
        相关内容
      </button>
    </div>
    <p v-if="selectionNotice" role="status">{{ selectionNotice }}</p>
    <div v-if="selected && !relatedTab" class="detail-body">
      <div class="detail-cover">
        <img
          v-if="selected.coverUrl && !workspace.preview"
          :src="selected.coverUrl"
          :alt="selected.title"
          @error="selected.coverUrl = ''"
        /><ReferenceArt
          v-else
          :name="selected.art"
          :alt="selected.title"
          :local="workspace.preview"
        /><span class="source-chip"
          ><span :class="['mini-mark', selected.platform]">{{
            platformMark(selected.platform)
          }}</span
          >{{ selected.platformLabel }}</span
        ><span v-if="selected.duration" class="duration">{{ selected.duration }}</span
        ><span v-if="!workspace.preview && !selected.coverUrl" class="cover-placeholder"
          >暂无封面</span
        >
      </div>
      <h2 class="detail-title">{{ selected.title }}</h2>
      <p class="detail-byline">
        <span class="tiny-avatar"
          ><ReferenceArt name="portrait" alt="" :local="workspace.preview" /></span
        >{{
          workspace.preview ? "森中人" : selected.creatorName || selected.accountName || "档案内容"
        }}<span>·</span>{{ selected.platformLabel }}<span>·</span>{{ selected.time }}
      </p>
      <p v-if="detail.error" class="detail-description" role="alert">{{ detail.error }}</p>
      <p v-else-if="workspace.preview" class="detail-description">{{ selected.body }}</p>
      <ArchiveDocument
        v-else
        class="detail-description"
        :document="detail.document"
        :media="detail.media"
        :body="detail.body"
      />
      <EntryMetaPanel v-if="!workspace.preview && detail.meta" :meta="detail.meta" />
      <details v-if="!workspace.preview && Object.keys(detail.metadata).length">
        <summary>来源信息与观察记录</summary>
        <pre class="metadata-text">{{ JSON.stringify(detail.metadata, null, 2) }}</pre>
        <pre class="metadata-text">{{ JSON.stringify(detail.observations, null, 2) }}</pre>
      </details>
      <ArchiveRemoval
        v-if="!workspace.preview && selectedId"
        :key="`remove-${selectedId}`"
        :entry-id="selectedId"
      />
      <AcquisitionHistory
        v-if="!workspace.preview && selectedId"
        :key="`acquisition-${selectedId}`"
        :entry-id="selectedId"
      />
      <ArchiveHistory
        v-if="!workspace.preview && selectedId"
        :key="selectedId"
        :entry-id="selectedId"
      />
      <QualityPanel
        v-if="!workspace.preview && selectedId && detail.media.length"
        :entry-id="selectedId"
      />
      <LiveRecording
        v-if="detail.live"
        :live="detail.live"
        :fragments="detail.fragments"
        @refresh="emit('refreshLive')"
      />
      <div class="entry-tags detail-tags">
        <button v-for="tag in selected.tags" :key="tag" @click="workspace.query = tag">
          # {{ tag }}
        </button>
      </div>
      <section class="detail-section">
        <h3>来源信息</h3>
        <div class="source-row">
          <TraceIcon name="play" :size="15" /><span>{{
            workspace.preview
              ? selected.platform === "bilibili"
                ? "https://www.bilibili.com/video/BV1..."
                : selected.platformLabel + " · 示例来源"
              : selected.sourceUrl || "来源标识：" + (selected.sourceKey || selected.id)
          }}</span
          ><TraceIcon name="external" :size="14" />
        </div>
      </section>
      <section class="detail-section">
        <h3>
          {{ workspace.preview ? "已保存到" : "保存状态"
          }}<small v-if="workspace.preview">示例</small>
        </h3>
        <div v-if="workspace.preview" class="saved-files">
          <div>
            <span class="file-icon"
              ><TraceIcon :name="selected.kind === 'video' ? 'play' : 'image'" :size="15" /></span
            >{{ selected.kind === "video" ? "视频" : "图片"
            }}<small>{{ selected.kind === "video" ? "1.2 GB" : "4.8 MB" }}</small>
          </div>
          <div>
            <span class="file-icon"><TraceIcon name="image" :size="15" /></span>封面图片<small
              >4.8 MB</small
            >
          </div>
          <div>
            <span class="file-icon"><TraceIcon name="file" :size="15" /></span>文案<small
              >1.3 KB</small
            >
          </div>
        </div>
        <div v-else class="saved-files">
          <p>{{ selected.saveLabel }}</p>
          <p>质量：{{ selected.qualityLabel }}</p>
          <small>发布时间精度：{{ timePrecisionLabel(selected.precision) }}</small
          ><MediaBrowser :key="selectedId ?? undefined" :media="detail.media" /><button
            v-if="detail.mediaCursor"
            :disabled="detail.mediaLoading"
            @click="emit('moreMedia')"
          >
            {{ detail.mediaLoading ? "正在读取…" : "加载更多资源" }}
          </button>
        </div>
      </section>
      <section class="detail-section">
        <h3>操作</h3>
        <div class="detail-actions">
          <button :disabled="!workspace.preview && !selected.sourceUrl" @click="emit('openSource')">
            <TraceIcon name="external" :size="18" />在浏览器中打开</button
          ><button v-if="workspace.preview" @click="emit('previewAction')">
            <TraceIcon name="download" :size="18" />下载到本地</button
          ><button v-if="workspace.preview" @click="emit('previewAction')">
            <TraceIcon name="more" :size="18" />更多操作<TraceIcon
              name="chevron"
              :size="14"
            /></button
          ><button v-else @click="emit('recheck')">补查此内容</button
          ><button v-if="!workspace.preview && selected.canFetch" @click="emit('fetch')">
            补下载未保存的视频</button
          ><router-link v-if="!workspace.preview" to="/timeline"
            >在内容库查看<TraceIcon name="chevron" :size="14"
          /></router-link>
        </div>
      </section>
      <section v-if="workspace.preview" class="detail-section related-section">
        <h3>相关内容</h3>
        <button
          v-for="entry in related.slice(0, 2)"
          :key="entry.id"
          class="related-item"
          @click="emit('pick', entry)"
        >
          <span class="related-cover"
            ><img
              v-if="entry.coverUrl && !workspace.preview"
              :src="entry.coverUrl"
              :alt="entry.title"
              loading="lazy"
              @error="entry.coverUrl = ''" /><ReferenceArt
              v-else
              :name="entry.art"
              :alt="entry.title"
              :local="workspace.preview" /></span
          ><span
            ><strong>{{ entry.title }}</strong
            ><small
              ><b :class="['mini-mark', entry.platform]">{{ platformMark(entry.platform) }}</b
              >{{ entry.platformLabel }} · {{ entry.time }}</small
            ></span
          >
        </button>
      </section>
    </div>
    <div v-else-if="relatedTab" class="detail-body">
      <p class="related-note">
        {{ workspace.preview ? "同一创作者的其他示例内容" : "当前档案中其他已载入的内容" }}
      </p>
      <button
        v-for="entry in related"
        :key="entry.id"
        class="related-item"
        @click="emit('pick', entry)"
      >
        <span class="related-cover"
          ><img
            v-if="entry.coverUrl && !workspace.preview"
            :src="entry.coverUrl"
            :alt="entry.title"
            loading="lazy"
            @error="entry.coverUrl = ''" /><ReferenceArt
            v-else
            :name="entry.art"
            :alt="entry.title"
            :local="workspace.preview" /></span
        ><span
          ><strong>{{ entry.title }}</strong
          ><small>{{ entry.platformLabel }} · {{ entry.time }}</small></span
        ></button
      ><n-empty v-if="!related.length" description="暂无相关内容" />
    </div>
    <div v-else class="detail-empty">
      <TraceIcon name="file" :size="30" />
      <p>选择一条内容，查看详情</p>
    </div>
  </aside>
</template>
