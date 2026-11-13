package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

// An error the page shows is also in the run log, with the endpoint path and
// the decoder's reason but never the signed query; repeats are written once.
func TestLogUserErrorWritesDetailOnce(t *testing.T) {
	b := &Backend{logPath: filepath.Join(t.TempDir(), "trace.log")}
	var target map[string]struct{ UID int64 }
	cause := json.Unmarshal([]byte(`[]`), &target)
	err := &site.Error{Site: site.Bilibili, Kind: site.ErrKindSchemaChanged, Message: "解析接口数据字段失败",
		Endpoint: "https://api.live.bilibili.com/room/v1/Room/get_status_info_by_uids?uids%5B%5D=1&w_rid=secret", Err: cause}
	b.LogUserError(err)
	b.LogUserError(err)
	b.LogUserError(errors.New("保存设置失败"))
	raw, rErr := os.ReadFile(b.logPath)
	if rErr != nil {
		t.Fatal(rErr)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %q", lines)
	}
	first := lines[0]
	for _, want := range []string{"[ERROR] ui: bilibili [schema_changed]: 解析接口数据字段失败", "endpoint=/room/v1/Room/get_status_info_by_uids", "cause=json: cannot unmarshal array"} {
		if !strings.Contains(first, want) {
			t.Fatalf("log line %q lacks %q", first, want)
		}
	}
	if strings.Contains(first, "w_rid") || strings.Contains(first, "?") {
		t.Fatalf("query leaked into the log: %q", first)
	}
	if !strings.Contains(lines[1], "ui: 保存设置失败") {
		t.Fatalf("plain error not logged: %q", lines[1])
	}
}
