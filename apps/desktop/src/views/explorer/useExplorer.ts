import { computed, onBeforeUnmount, reactive, ref } from "vue";
import { api, type ExplorerNode, type ListView } from "../../lib/api";

export const PAGE_SIZE = 100;

interface Branch {
  nodes: ExplorerNode[];
  truncated: boolean;
  state: "loading" | "ready" | "error";
  error: string;
}
export interface VisibleNode {
  node: ExplorerNode;
  depth: number;
  expanded: boolean;
  loading: boolean;
}

/** A node address of the download side (`dl` or `dl/...`), whose files live outside the library. */
export const isDownloadSide = (node: string) => node === "dl" || node.startsWith("dl/");

/**
 * The folder tree and the sorted, filtered, paged file list of the resource explorer. Every
 * response that is not from the latest request is dropped, so a slow answer never overwrites
 * what the user chose since.
 */
export function useExplorer() {
  const branches = reactive<Record<string, Branch>>({});
  const expanded = reactive<Record<string, boolean>>({});
  const rootState = reactive<Branch>({ nodes: [], truncated: false, state: "loading", error: "" });
  const selected = ref<ExplorerNode | null>(null);
  const list = ref<ListView | null>(null);
  const listError = ref("");
  const listLoading = ref(false);
  const sort = ref("");
  const descending = ref(false);
  const query = ref("");
  const kind = ref("");
  const state = ref("");
  const offset = ref(0);

  let listRequest = 0;
  let alive = true;
  let timer: ReturnType<typeof setTimeout> | undefined;
  onBeforeUnmount(() => {
    alive = false;
    ++listRequest;
    clearTimeout(timer);
  });

  async function loadRoots() {
    rootState.state = "loading";
    try {
      const view = await api.explorerTree();
      if (!alive) return;
      rootState.nodes = view.nodes;
      rootState.truncated = view.truncated;
      rootState.state = "ready";
      rootState.error = "";
      if (!selected.value && view.nodes[0]) await select(view.nodes[0]);
    } catch (e) {
      if (!alive) return;
      rootState.state = "error";
      rootState.error = String(e);
    }
  }

  async function loadBranch(node: ExplorerNode) {
    // Read the branch back through the reactive map: a plain object assigned into it is not tracked.
    branches[node.node] ??= { nodes: [], truncated: false, state: "loading", error: "" };
    const branch = branches[node.node]!;
    branch.state = "loading";
    try {
      const view = await api.explorerTree(node.node);
      if (!alive) return;
      branch.nodes = view.nodes;
      branch.truncated = view.truncated;
      branch.state = "ready";
      branch.error = "";
    } catch (e) {
      if (!alive) return;
      branch.state = "error";
      branch.error = String(e);
    }
  }

  async function toggle(node: ExplorerNode) {
    if (!node.expandable) return;
    if (expanded[node.node]) {
      expanded[node.node] = false;
      return;
    }
    expanded[node.node] = true;
    if (branches[node.node]?.state !== "ready" && branches[node.node]?.state !== "loading")
      await loadBranch(node);
  }

  const visible = computed<VisibleNode[]>(() => {
    const out: VisibleNode[] = [];
    const walk = (nodes: ExplorerNode[], depth: number) => {
      for (const node of nodes) {
        const open = Boolean(expanded[node.node]);
        out.push({
          node,
          depth,
          expanded: open,
          loading: branches[node.node]?.state === "loading",
        });
        const branch = branches[node.node];
        if (open && branch) walk(branch.nodes, depth + 1);
      }
    };
    walk(rootState.nodes, 0);
    return out;
  });

  async function loadList() {
    const node = selected.value;
    if (!node) return;
    const current = ++listRequest;
    listLoading.value = true;
    listError.value = "";
    try {
      const view = await api.explorerList({
        node: node.node,
        sort: sort.value || undefined,
        descending: descending.value,
        query: query.value,
        kind: kind.value,
        state: state.value,
        offset: offset.value,
        limit: PAGE_SIZE,
      });
      if (current === listRequest && alive) list.value = view;
    } catch (e) {
      if (current === listRequest && alive) {
        listError.value = String(e);
        list.value = null;
      }
    } finally {
      if (current === listRequest && alive) listLoading.value = false;
    }
  }

  async function select(node: ExplorerNode) {
    if (selected.value?.node !== node.node) {
      query.value = "";
      kind.value = "";
      state.value = "";
      offset.value = 0;
      sort.value = "";
      descending.value = false;
    }
    selected.value = node;
    await loadList();
  }

  /** Changing sort, direction or a filter goes back to the first page. */
  function refilter(delay = 0) {
    offset.value = 0;
    clearTimeout(timer);
    ++listRequest;
    if (delay <= 0) void loadList();
    else timer = setTimeout(() => void loadList(), delay);
  }
  function sortBy(key: string) {
    if (sort.value === key) descending.value = !descending.value;
    else {
      sort.value = key;
      descending.value = false;
    }
    refilter();
  }
  function page(direction: 1 | -1) {
    const total = list.value?.total ?? 0;
    const next = offset.value + direction * PAGE_SIZE;
    if (next < 0 || next >= Math.max(total, 1)) return;
    offset.value = next;
    void loadList();
  }

  return {
    rootState,
    branches,
    visible,
    selected,
    list,
    listError,
    listLoading,
    sort,
    descending,
    query,
    kind,
    state,
    offset,
    loadRoots,
    toggle,
    select,
    loadList,
    refilter,
    sortBy,
    page,
  };
}
