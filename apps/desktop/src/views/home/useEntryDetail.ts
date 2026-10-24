import { onBeforeUnmount, reactive, type Ref } from "vue";
import {
  api,
  type DocumentDto,
  type EntryMetaDto,
  type FragmentDto,
  type LiveDto,
  type MediaDto,
  type RevisionDto,
} from "../../lib/api";
import { mergeMedia, readRevision } from "../../lib/archive";
import { useWorkspaceStore } from "../../stores/workspace";

/** Everything the detail column shows for the selected entry. */
export interface EntryDetailState {
  error: string;
  body: string;
  document?: DocumentDto;
  live?: LiveDto;
  fragments: FragmentDto[];
  metadata: Record<string, unknown>;
  meta: EntryMetaDto | null;
  observations: unknown[];
  media: MediaDto[];
  mediaCursor: string | null;
  revisionId?: string;
  mediaLoading: boolean;
}

function emptyDetail(): EntryDetailState {
  return {
    error: "",
    body: "",
    document: undefined,
    live: undefined,
    fragments: [],
    metadata: {},
    meta: null,
    observations: [],
    media: [],
    mediaCursor: null,
    revisionId: undefined,
    mediaLoading: false,
  };
}

/**
 * Loads the revision, media pages and live fragments of the selected entry. A response that
 * arrives after the selection changed or the view was left is dropped.
 */
export function useEntryDetail(selectedId: Ref<string | null>) {
  const workspace = useWorkspaceStore();
  const detail = reactive<EntryDetailState>(emptyDetail());
  let requestId = 0;
  let mediaPages = 1;
  let alive = true;
  onBeforeUnmount(() => {
    alive = false;
    ++requestId;
  });

  async function loadDetail(id: string | null, preserve = false) {
    const current = ++requestId;
    detail.error = "";
    if (!preserve) {
      detail.body = "";
      detail.media = [];
      detail.document = undefined;
      detail.live = undefined;
      detail.fragments = [];
      detail.metadata = {};
      detail.meta = null;
      detail.observations = [];
      detail.mediaCursor = null;
      detail.revisionId = undefined;
      mediaPages = 1;
      detail.mediaLoading = false;
    }
    if (!id || workspace.preview) return;
    try {
      const result = await api.archiveEntryGet(id);
      const isCurrent = () => alive && current === requestId && selectedId.value === id;
      const revisions = result.revisions as RevisionDto[];
      const revisionId = (result.entry as { current_revision_id?: string }).current_revision_id;
      let revision =
        revisions.find((r) => r.revision_id === revisionId) ??
        [...revisions].reverse().find((r) => r.completeness === "full") ??
        revisions.at(-1);
      if (revision) revision = await readRevision(id, revision, isCurrent);
      let media = result.media ?? [];
      let cursor = result.media_cursor ?? null;
      const pages = preserve ? mediaPages : 1;
      for (let page = 1; page < pages && cursor; page++) {
        if (!isCurrent()) return;
        const next = await api.archiveEntryPage<MediaDto>(id, "media", cursor, revisionId);
        media = mergeMedia(media, next.items);
        cursor = next.next_cursor;
      }
      if (alive && current === requestId && selectedId.value === id) {
        detail.body =
          revision?.body ?? String((result.entry as Record<string, unknown>).body ?? "");
        detail.metadata = revision?.metadata ?? {};
        detail.meta = result.meta ?? null;
        detail.observations = result.observations;
        detail.document = revision?.document;
        detail.media = media;
        detail.mediaCursor = cursor;
        detail.revisionId = revisionId;
        detail.live = result.live ?? undefined;
        detail.fragments = result.fragments ?? [];
      }
    } catch {
      if (alive && current === requestId) detail.error = "详情读取失败，请重新选择条目重试。";
    }
  }
  async function moreMedia() {
    const id = selectedId.value,
      cursor = detail.mediaCursor,
      current = requestId;
    if (!id || !cursor || detail.mediaLoading) return;
    detail.mediaLoading = true;
    try {
      const page = await api.archiveEntryPage<MediaDto>(id, "media", cursor, detail.revisionId);
      if (alive && current === requestId) {
        detail.media = mergeMedia(detail.media, page.items);
        detail.mediaCursor = page.next_cursor;
        mediaPages++;
      }
    } catch {
      if (alive && current === requestId) detail.error = "更多资源读取失败，请重试。";
    } finally {
      if (alive && current === requestId) detail.mediaLoading = false;
    }
  }

  return { detail, loadDetail, moreMedia };
}
