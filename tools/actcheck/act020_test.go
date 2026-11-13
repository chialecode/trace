package main

import (
	"net/http"
	"testing"
)

func TestAct020ClassifyAndHandles(t *testing.T) {
	for path, want := range map[string]string{
		"https://api.bilibili.com/x/v3/fav/resource/list":           "bili_fav_resources",
		"https://api.bilibili.com/x/v2/history/toview/web":          "bili_toview",
		"https://api.bilibili.com/x/web-interface/history/cursor":   "bili_history",
		"https://www.douyin.com/aweme/v1/web/aweme/listcollection/": "dy_listcollection",
		"https://www.douyin.com/aweme/v1/web/history/read/":         "dy_history_read",
		"https://i0.hdslb.com/bfs/archive/cover.jpg":                "",
		"https://www.douyin.com/aweme/v1/web/aweme/post/":           "",
		"https://api.bilibili.com/x/v3/fav/resource/batch-deal":     "",
	} {
		r, _ := http.NewRequest(http.MethodGet, path, nil)
		if _, got := act020Classify(r); got != want {
			t.Errorf("%s -> %q want %q", path, got, want)
		}
		if want != "" && act020Limits[want] == 0 {
			t.Errorf("%s has no ledger limit", want)
		}
	}
	total := 0
	for _, n := range act020Limits {
		total += n
	}
	if total != act020MaxAPI {
		t.Fatalf("category limits sum to %d, total is %d", total, act020MaxAPI)
	}
	r := newAct010Redactor()
	r.addHandles("我的私密收藏夹名字", "ab")
	if act020NoHandles(r, `{"facts":{"items":3}}`) != nil {
		t.Fatal("clean receipt flagged")
	}
	if act020NoHandles(r, `{"error":"我的私密收藏夹名字"}`) == nil {
		t.Fatal("title in receipt not caught")
	}
	if got := act020Scrub("douyin [risk_control http=200]: 抖音返回空响应 https://www.douyin.com/aweme/v1/web/aweme/favorite/?a_bogus=x"); got != "douyin [risk_control http=200]: 抖音返回空响应 [URL]" {
		t.Fatalf("scrub = %q", got)
	}
}
