// Synthetic Vite x-web lazy ES module chunk fixture (zero real values).
const baseNode = { kind: "Request" };
const symTag = "synth_tag";
const queryId = "SynthViteLazyBookmarksQid06";
const switchList = [
  "graphql_timeline_v2_bookmark_timeline",
  "responsive_web_graphql_exclude_directive_enabled",
  "longform_notetweets_consumption_enabled",
];
const metadata = {
  featureSwitches: switchList,
  fieldToggles: ["withArticleRichContentState"],
};

export const BookmarksLazyRequest = {
  ...baseNode,
  [symTag]: 1,
  toJSON() {
    return "Bookmarks";
  },
  params: {
    id: queryId,
    name: `Bookmarks`,
    operationKind: "query",
    metadata,
  },
};
