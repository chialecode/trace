<script setup lang="ts">
import { NButton, NEmpty, NSpin } from "naive-ui";
import TraceIcon from "../../components/TraceIcon.vue";
import ReferenceArt from "../../components/ReferenceArt.vue";
import { useWorkspaceStore } from "../../stores/workspace";
import { platformMark, type FeedEntry } from "../../lib/preview";

defineProps<{
  groups: { day: string; items: FeedEntry[] }[];
  /** Entries before filtering (decides the empty-state wording). */
  entryCount: number;
  /** Entries after filtering. */
  shown: number;
  loading: boolean;
  error: string;
  hasMore: boolean;
  focusedEntry?: string;
  selectedId: string | null;
}>();
const emit = defineEmits<{
  select: [entry: FeedEntry];
  reset: [];
  retry: [];
  fetch: [entry: FeedEntry];
  more: [];
  previewAction: [];
}>();
const workspace = useWorkspaceStore();
</script>

<template>
  <n-spin :show="loading">
    <p v-if="focusedEntry" class="selection-notice">
      当前定位到指定发布记录。<button @click="emit('reset')">返回完整时间线</button>
    </p>
    <div v-if="error" class="feed-state" role="alert">
      <h3>无法连接档案</h3>
      <p>{{ error }}</p>
      <n-button @click="emit('retry')">重试</n-button
      ><router-link to="/status">查看后台状态</router-link>
    </div>
    <div v-else-if="!loading && !shown" class="feed-state">
      <n-empty :description="entryCount ? '没有符合条件的内容' : '还没有收录内容'" /><n-button
        v-if="entryCount"
        @click="emit('reset')"
        >查看全部内容</n-button
      ><router-link v-else to="/creators">添加第一个创作者</router-link>
    </div>
    <div v-else class="timeline">
      <section v-for="(group, index) in groups" :key="group.day" class="day-group">
        <h2 class="day-heading">
          <span class="day-square"></span
          ><strong>{{
            workspace.preview ? (group.day === "2025-09-24" ? "今天" : "昨天") : "发布于"
          }}</strong
          ><time>{{ group.day }}</time
          ><span v-if="workspace.preview && index === 0" class="sample-date">示例时间线</span>
        </h2>
        <div class="timeline-track">
          <article
            v-for="entry in group.items"
            :key="entry.id"
            :class="[
              'feed-card',
              { selected: selectedId === entry.id, compact: entry.kind !== 'video' },
            ]"
          >
            <span class="timeline-dot"></span>
            <div class="entry-platform">
              <span :class="['platform-mark', entry.platform]">{{
                platformMark(entry.platform)
              }}</span
              ><span>{{ entry.platformLabel }}</span
              ><time>{{ entry.time }}</time
              ><button
                class="icon-button card-more"
                :aria-label="'查看详情：' + entry.title"
                @click="emit('select', entry)"
              >
                <TraceIcon name="more" :size="17" />
              </button>
            </div>
            <div class="entry-content">
              <button
                class="entry-copy"
                @click="emit('select', entry)"
                :aria-label="'查看：' + entry.title"
              >
                <h3>{{ entry.title }}</h3>
                <p v-if="entry.body">{{ entry.body }}</p>
                <p v-if="!workspace.preview">{{ entry.creatorName }} · {{ entry.accountName }}</p>
                <p v-if="!workspace.preview">
                  {{ entry.saveLabel }} · {{ entry.qualityLabel }} ·
                  {{ entry.authenticationLabel }}
                </p>
                <p v-if="entry.contentLine || entry.statsLine || entry.whereLine" class="meta-line">
                  {{
                    [entry.contentLine, entry.statsLine, entry.whereLine]
                      .filter(Boolean)
                      .join(" · ")
                  }}
                </p>
              </button>
              <button
                class="entry-cover"
                @click="emit('select', entry)"
                :aria-label="'选择内容：' + entry.title"
              >
                <img
                  v-if="entry.coverUrl && !workspace.preview"
                  :src="entry.coverUrl"
                  :alt="entry.title"
                  loading="lazy"
                  @error="entry.coverUrl = ''"
                /><ReferenceArt
                  v-else
                  :name="entry.art"
                  :alt="entry.title"
                  :local="workspace.preview"
                /><span v-if="entry.duration" class="duration">{{ entry.duration }}</span
                ><span v-if="!workspace.preview && !entry.coverUrl" class="cover-placeholder"
                  >暂无封面</span
                >
              </button>
              <div v-if="entry.canFetch && !workspace.preview" class="entry-fetch">
                <button :aria-label="'补下载：' + entry.title" @click="emit('fetch', entry)">
                  <TraceIcon name="download" :size="15" />补下载
                </button>
              </div>
              <div class="entry-tags">
                <button v-for="tag in entry.tags" :key="tag" @click="workspace.query = tag">
                  # {{ tag }}
                </button>
              </div>
              <div v-if="workspace.preview && entry.views" class="entry-metrics">
                <span><TraceIcon name="play" :size="15" />{{ entry.views }}</span
                ><span><TraceIcon name="chat" :size="15" />{{ entry.comments }}</span
                ><span><TraceIcon name="heart" :size="16" />{{ entry.likes }}</span
                ><button
                  class="icon-button"
                  :aria-label="'下载示例：' + entry.title"
                  @click="emit('previewAction')"
                >
                  <TraceIcon name="download" :size="16" />
                </button>
              </div>
            </div>
          </article>
        </div>
      </section>
    </div>
  </n-spin>
  <button
    v-if="hasMore && !workspace.preview"
    class="more-content"
    :disabled="loading"
    @click="emit('more')"
  >
    加载更多内容 <TraceIcon name="chevron" :size="15" />
  </button>
</template>
