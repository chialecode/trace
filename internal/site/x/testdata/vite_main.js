// Synthetic 2026 Vite x-web ES module bundle fixture (zero real values).
export const webBearer = "SYNTHETIC_VITE_BEARER_TOKEN_0002";
const loadTx = () => import("./ondemand.s-vite02.js");

const featureDefaults = {
  responsive_web_graphql_exclude_directive_enabled: true,
  creator_subscriptions_tweet_preview_api_enabled: true,
  longform_notetweets_consumption_enabled: !0,
  verified_phone_label_enabled: false,
  withAuxiliaryUserLabels: !1,
};

export const UserByScreenNameQuery = {
  params: {
    id: "SynthViteQidUserByScreenName01",
    name: "UserByScreenName",
    operationKind: "query",
    metadata: {
      featureSwitches: [
        "responsive_web_graphql_exclude_directive_enabled",
        "verified_phone_label_enabled",
      ],
      fieldToggles: ["withAuxiliaryUserLabels"],
    },
  },
};

export const UserByRestIdQuery = {
  params: {
    id: "SynthViteQidUserByRestId02",
    name: "UserByRestId",
    operationKind: "query",
    metadata: {
      featureSwitches: [
        "responsive_web_graphql_exclude_directive_enabled",
        "creator_subscriptions_tweet_preview_api_enabled",
      ],
      fieldToggles: ["withAuxiliaryUserLabels"],
    },
  },
};

export const UserTweetsQuery = {
  queryId: "SynthViteQidUserTweets03",
  operationName: "UserTweets",
  operationType: "query",
  features: {
    responsive_web_graphql_exclude_directive_enabled: true,
    longform_notetweets_consumption_enabled: !0,
    verified_phone_label_enabled: !1,
  },
  fieldToggles: {
    withArticlePlainText: false,
  },
};

export const TweetDetailQuery = {
  queryId: "SynthViteQidTweetDetail04",
  operationName: "TweetDetail",
  operationType: "query",
  features: {
    responsive_web_graphql_exclude_directive_enabled: true,
    creator_subscriptions_tweet_preview_api_enabled: true,
    longform_notetweets_consumption_enabled: true,
  },
  fieldToggles: {
    withArticleRichContentState: !0,
    withArticlePlainText: !1,
  },
};

export const FollowingQuery = {
  params: {
    id: "SynthViteQidFollowing05",
    name: "Following",
    operationKind: "query",
    metadata: {
      featureSwitches: ["responsive_web_graphql_exclude_directive_enabled"],
    },
  },
};

export const BookmarksQuery = {
  params: {
    id: "SynthViteQidBookmarks06",
    name: "Bookmarks",
    operationKind: "query",
    metadata: {
      featureSwitches: [
        "responsive_web_graphql_exclude_directive_enabled",
        "longform_notetweets_consumption_enabled",
      ],
    },
  },
};

export const LikesQuery = {
  queryId: "SynthViteQidLikes07",
  operationName: "Likes",
  operationType: "query",
  features: {
    responsive_web_graphql_exclude_directive_enabled: true,
    verified_phone_label_enabled: false,
  },
};

export const UserTweetsAndRepliesQuery = {
  queryId: "SynthViteQidUserTweetsAndReplies08",
  operationName: "UserTweetsAndReplies",
  operationType: "query",
  features: {
    responsive_web_graphql_exclude_directive_enabled: true,
    longform_notetweets_consumption_enabled: !0,
    verified_phone_label_enabled: !1,
  },
  fieldToggles: {
    withArticlePlainText: false,
  },
};

export const UserMediaQuery = {
  queryId: "SynthViteQidUserMedia09",
  operationName: "UserMedia",
  operationType: "query",
  features: {
    responsive_web_graphql_exclude_directive_enabled: true,
    longform_notetweets_consumption_enabled: !0,
    verified_phone_label_enabled: !1,
  },
  fieldToggles: {
    withArticlePlainText: false,
  },
};

export const UserHighlightsTweetsQuery = {
  queryId: "SynthViteQidUserHighlightsTweets10",
  operationName: "UserHighlightsTweets",
  operationType: "query",
  features: {
    responsive_web_graphql_exclude_directive_enabled: true,
    longform_notetweets_consumption_enabled: !0,
    verified_phone_label_enabled: !1,
  },
  fieldToggles: {
    withArticlePlainText: false,
  },
};

export const UserArticlesTweetsQuery = {
  queryId: "SynthViteQidUserArticlesTweets11",
  operationName: "UserArticlesTweets",
  operationType: "query",
  features: {
    responsive_web_graphql_exclude_directive_enabled: true,
    longform_notetweets_consumption_enabled: !0,
    verified_phone_label_enabled: !1,
  },
  fieldToggles: {
    withArticleRichContentState: !0,
    withArticlePlainText: !0,
  },
};

export { loadTx };
