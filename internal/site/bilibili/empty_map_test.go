package bilibili

import (
	"context"
	"net/http"
	"testing"
)

// get_status_info_by_uids answers `"data": []` (PHP's empty map) when no
// requested UID has a live room; opening such a creator's space showed
// "解析接口数据字段失败" (seen 2026-11-16 on a real space page).
func TestLiveStatusByUIDsAcceptsEmptyArrayData(t *testing.T) {
	c, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","message":"success","data":[]}`))
	}), 0)
	got, err := c.LiveStatusByUIDs(context.Background(), []int64{702699638})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty array data: %v, %v", got, err)
	}
}

// A non-empty array where an object is expected is still a schema change.
func TestEmptyArrayToleranceOnlyCoversEmptyMaps(t *testing.T) {
	var m map[string]int
	if !emptyArrayForMap([]byte(" [ ] "), &m) {
		t.Fatal("empty array for a map not accepted")
	}
	if emptyArrayForMap([]byte(`[1]`), &m) {
		t.Fatal("non-empty array accepted as an empty map")
	}
	var s struct{ A int }
	if emptyArrayForMap([]byte(`[]`), &s) {
		t.Fatal("empty array accepted for a struct")
	}
	c, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":[{"uid":1}]}`))
	}), 0)
	if _, err := c.LiveStatusByUIDs(context.Background(), []int64{1}); !IsKind(err, ErrKindSchemaChanged) {
		t.Fatalf("non-empty array: %v", err)
	}
}
