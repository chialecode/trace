<script setup lang="ts">
import { ref, onBeforeUnmount } from "vue";
import { api, type RevisionDto, type MediaDto } from "../lib/api";
import { readRevision, mergeMedia } from "../lib/archive";
import ArchiveDocument from "./ArchiveDocument.vue";
import MediaBrowser from "./MediaBrowser.vue";
const props = defineProps<{ entryId: string }>();
const revisions = ref<RevisionDto[]>([]),
  observations = ref<{ seq: number; observed_at?: string; access_state?: string }[]>([]),
  selected = ref<RevisionDto>(),
  media = ref<MediaDto[]>([]);
const cursors = ref<{
  revisions?: string | null;
  observations?: string | null;
  media?: string | null;
}>({});
const evidence = ref<unknown>();
const busy = ref(false),
  error = ref("");
let generation = 0;
let alive = true;
async function load(section: "revisions" | "observations", older = false) {
  const current = ++generation;
  busy.value = true;
  error.value = "";
  try {
    const page = await api.archiveEntryPage<RevisionDto>(
      props.entryId,
      section,
      older ? cursors.value[section] : null,
    );
    if (!alive || current !== generation) return;
    if (section === "revisions") {
      const all = new Map(revisions.value.map((r) => [r.revision_id, r]));
      for (const r of page.items) all.set(r.revision_id, r);
      revisions.value = [...all.values()];
    } else {
      const all = new Map(observations.value.map((r) => [r.seq, r]));
      for (const raw of page.items) {
        const r = raw as unknown as { seq: number };
        all.set(r.seq, r);
      }
      observations.value = [...all.values()];
    }
    if (older || cursors.value[section] === undefined) cursors.value[section] = page.next_cursor;
  } catch {
    if (alive && current === generation) error.value = "历史读取失败，请重试。";
  } finally {
    if (alive && current === generation) busy.value = false;
  }
}
async function choose(revision: RevisionDto) {
  const current = ++generation;
  busy.value = true;
  error.value = "";
  selected.value = undefined;
  media.value = [];
  try {
    const [full, page] = await Promise.all([
      readRevision(props.entryId, revision, () => alive && current === generation),
      api.archiveEntryPage<MediaDto>(props.entryId, "media", null, revision.revision_id),
    ]);
    if (!alive || current !== generation) return;
    selected.value = full;
    media.value = page.items;
    cursors.value.media = page.next_cursor;
  } catch {
    if (alive && current === generation) error.value = "历史正文读取失败，请重试。";
  } finally {
    if (alive && current === generation) busy.value = false;
  }
}
async function moreMedia() {
  if (!selected.value || !cursors.value.media) return;
  const current = ++generation;
  busy.value = true;
  error.value = "";
  try {
    const page = await api.archiveEntryPage<MediaDto>(
      props.entryId,
      "media",
      cursors.value.media,
      selected.value.revision_id,
    );
    if (!alive || current !== generation) return;
    media.value = mergeMedia(media.value, page.items);
    cursors.value.media = page.next_cursor;
  } catch {
    if (alive && current === generation) error.value = "历史资源读取失败，请重试。";
  } finally {
    if (alive && current === generation) busy.value = false;
  }
}
async function readObservation(seq: number) {
  const current = ++generation;
  busy.value = true;
  error.value = "";
  evidence.value = undefined;
  try {
    let offset = 0;
    let text = "";
    while (true) {
      const part = await api.archiveObservationRead(props.entryId, seq, offset);
      if (!alive || current !== generation) return;
      text += part.text;
      if (part.next_offset === null) break;
      if (part.next_offset <= offset) throw new Error("invalid continuation");
      offset = part.next_offset;
    }
    evidence.value = JSON.parse(text);
  } catch {
    if (alive && current === generation) error.value = "观察详情读取失败，请重试。";
  } finally {
    if (alive && current === generation) busy.value = false;
  }
}
onBeforeUnmount(() => {
  alive = false;
  generation++;
});
</script>
<template>
  <details class="archive-history">
    <summary>历史版本与全部观察</summary>
    <button :disabled="busy" @click="load('revisions')">读取版本</button
    ><button :disabled="busy" @click="load('observations')">读取观察记录</button>
    <p v-if="error" role="alert">{{ error }}</p>
    <ul>
      <li v-for="revision in revisions" :key="revision.revision_id">
        <button :disabled="busy" @click="choose(revision)">
          {{ revision.observed_at }} · {{ revision.title }} ·
          {{ revision.completeness === "full" ? "全文" : "摘要" }}
        </button>
      </li>
    </ul>
    <button v-if="cursors.revisions" :disabled="busy" @click="load('revisions', true)">
      更早版本
    </button>
    <ul>
      <li v-for="observation in observations" :key="observation.seq">
        <button :disabled="busy" @click="readObservation(observation.seq)">
          {{ observation.observed_at }} · {{ observation.access_state }} · 查看统计与证据
        </button>
      </li>
    </ul>
    <pre v-if="evidence">{{ JSON.stringify(evidence, null, 2) }}</pre>
    <button v-if="cursors.observations" :disabled="busy" @click="load('observations', true)">
      更早观察
    </button>
    <section v-if="selected" aria-label="历史版本正文">
      <h3>{{ selected.title }}</h3>
      <ArchiveDocument
        :document="selected.document"
        :body="selected.body ?? ''"
        :media="media"
      /><MediaBrowser :media="media" />
      <pre>{{ JSON.stringify(selected.metadata, null, 2) }}</pre>
      <button v-if="cursors.media" :disabled="busy" @click="moreMedia">更多历史资源</button>
    </section>
  </details>
</template>
<style scoped>
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 280px;
  overflow: auto;
}
button {
  margin: 5px;
  padding: 6px;
  border: 1px solid #dce4ee;
  border-radius: 6px;
  background: #f5f8ff;
  color: #245fc8;
}
ul {
  padding-left: 16px;
}
li {
  font-size: 0.75rem;
}
</style>
