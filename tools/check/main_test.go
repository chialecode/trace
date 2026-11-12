package main

import (
	"strings"
	"testing"
)

func TestDependencyDirectionRulesSynthetic(t *testing.T) {
	cases := []struct {
		name      string
		record    string
		wantError string
	}{
		// Rule 1: MyGo UI boundary (INV-05)
		{
			name:   "mygo allowed in internal/ui",
			record: "github.com/chialecode/trace/internal/ui|github.com/egoist/mygo,github.com/chialecode/trace/internal/service",
		},
		{
			name:      "mygo rejected in internal/service",
			record:    "github.com/chialecode/trace/internal/service|github.com/egoist/mygo",
			wantError: "INV-05 violation",
		},

		// Rule 2: internal/site stdlib-only
		{
			name:   "internal/site stdlib only accepted",
			record: "github.com/chialecode/trace/internal/site|context,crypto/sha256,encoding/hex,errors,fmt,net/http,net/url,regexp,strconv,strings,sync,unicode|context,crypto/internal/entropy/v1.0.0,vendor/golang.org/x/net/idna",
		},
		{
			name:      "internal/site importing internal package rejected",
			record:    "github.com/chialecode/trace/internal/site|context,github.com/chialecode/trace/internal/credential",
			wantError: "site stdlib-only violation",
		},
		{
			name:      "internal/site importing external module rejected",
			record:    "github.com/chialecode/trace/internal/site|context,golang.org/x/net/html",
			wantError: "site stdlib-only violation",
		},

		// Rule 3: internal/site/bilibili forbidden upper layers & cross-site douyin
		{
			name:   "internal/site/bilibili allowed deps accepted",
			record: "github.com/chialecode/trace/internal/site/bilibili|context,net/http,github.com/chialecode/trace/internal/site,github.com/chialecode/trace/internal/credential",
		},
		{
			name:      "internal/site/bilibili depending on storage rejected",
			record:    "github.com/chialecode/trace/internal/site/bilibili|github.com/chialecode/trace/internal/storage",
			wantError: "layer violation",
		},
		{
			name:      "internal/site/bilibili depending on douyin rejected",
			record:    "github.com/chialecode/trace/internal/site/bilibili|github.com/chialecode/trace/internal/site/douyin",
			wantError: "cross-site violation",
		},

		// Rule 4: internal/site/douyin forbidden upper layers & cross-site bilibili
		{
			name:   "internal/site/douyin allowed deps accepted",
			record: "github.com/chialecode/trace/internal/site/douyin|context,net/http,github.com/chialecode/trace/internal/site,github.com/chialecode/trace/internal/credential,github.com/chialecode/trace/internal/site/douyin/sign",
		},
		{
			name:      "internal/site/douyin depending on download rejected",
			record:    "github.com/chialecode/trace/internal/site/douyin|github.com/chialecode/trace/internal/download",
			wantError: "layer violation",
		},
		{
			name:      "internal/site/douyin depending on bilibili rejected",
			record:    "github.com/chialecode/trace/internal/site/douyin|github.com/chialecode/trace/internal/site/bilibili",
			wantError: "cross-site violation",
		},

		// Rule 5: internal/site/douyin/sign pure computation (no credential, no net/http, no bilibili/upper)
		{
			name:   "internal/site/douyin/sign pure stdlib accepted",
			record: "github.com/chialecode/trace/internal/site/douyin/sign|bytes,crypto/rc4,encoding/binary,fmt,math/rand,strings,time",
		},
		{
			name:      "internal/site/douyin/sign depending on credential rejected",
			record:    "github.com/chialecode/trace/internal/site/douyin/sign|bytes,github.com/chialecode/trace/internal/credential",
			wantError: "sign purity violation",
		},
		{
			name:      "internal/site/douyin/sign depending on net/http rejected",
			record:    "github.com/chialecode/trace/internal/site/douyin/sign|bytes,net/http",
			wantError: "sign purity violation",
		},

		// Rule 6: download/live/subscription/library/storage/settings/uistate cannot depend on douyin;
		// existing bilibili importers allowed, new bilibili importers rejected.
		{
			name:   "internal/download using internal/site and legacy bilibili accepted",
			record: "github.com/chialecode/trace/internal/download|github.com/chialecode/trace/internal/site,github.com/chialecode/trace/internal/site/bilibili|github.com/chialecode/trace/internal/site,github.com/chialecode/trace/internal/site/bilibili",
		},
		{
			name:      "internal/download depending on douyin rejected",
			record:    "github.com/chialecode/trace/internal/download|github.com/chialecode/trace/internal/site/douyin",
			wantError: "decoupling violation",
		},
		{
			name:      "internal/live depending on douyin rejected",
			record:    "github.com/chialecode/trace/internal/live|github.com/chialecode/trace/internal/site/douyin",
			wantError: "decoupling violation",
		},
		{
			name:      "internal/subscription depending on douyin rejected",
			record:    "github.com/chialecode/trace/internal/subscription|github.com/chialecode/trace/internal/site/douyin",
			wantError: "decoupling violation",
		},
		{
			name:      "internal/library depending on douyin rejected",
			record:    "github.com/chialecode/trace/internal/library|github.com/chialecode/trace/internal/site/douyin",
			wantError: "decoupling violation",
		},
		{
			name:   "internal/storage using internal/site accepted",
			record: "github.com/chialecode/trace/internal/storage|database/sql,github.com/chialecode/trace/internal/site",
		},
		{
			name:      "internal/storage adding bilibili dependency rejected",
			record:    "github.com/chialecode/trace/internal/storage|database/sql,github.com/chialecode/trace/internal/site/bilibili",
			wantError: "bilibili",
		},
		{
			name:      "internal/settings adding bilibili dependency rejected",
			record:    "github.com/chialecode/trace/internal/settings|github.com/chialecode/trace/internal/site/bilibili",
			wantError: "bilibili",
		},
		{
			name:      "internal/uistate adding douyin dependency rejected",
			record:    "github.com/chialecode/trace/internal/uistate|github.com/chialecode/trace/internal/site/douyin",
			wantError: "decoupling violation",
		},
		{
			name:      "new subpackage importing bilibili rejected",
			record:    "github.com/chialecode/trace/internal/player|github.com/chialecode/trace/internal/site/bilibili|github.com/chialecode/trace/internal/site/bilibili",
			wantError: "locked bilibili-importer violation",
		},

		// Rule 7: internal/ui cannot directly import internal/site/douyin or sign,
		// while transitive dependency via internal/service is allowed.
		{
			name:   "internal/ui using internal/service (which transitively uses douyin) accepted",
			record: "github.com/chialecode/trace/internal/ui|github.com/chialecode/trace/internal/service,github.com/chialecode/trace/internal/site/bilibili|github.com/chialecode/trace/internal/service,github.com/chialecode/trace/internal/site/bilibili,github.com/chialecode/trace/internal/site/douyin,github.com/chialecode/trace/internal/site/douyin/sign",
		},
		{
			name:      "internal/ui directly importing internal/site/douyin rejected",
			record:    "github.com/chialecode/trace/internal/ui|github.com/chialecode/trace/internal/service,github.com/chialecode/trace/internal/site/douyin|github.com/chialecode/trace/internal/service,github.com/chialecode/trace/internal/site/douyin",
			wantError: "ui decoupling violation",
		},
		{
			name:      "internal/ui directly importing internal/site/douyin/sign rejected",
			record:    "github.com/chialecode/trace/internal/ui|github.com/chialecode/trace/internal/site/douyin/sign|github.com/chialecode/trace/internal/site/douyin/sign",
			wantError: "ui decoupling violation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDependencyOutput(tc.record)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("expected record to pass, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantError)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantError)
			}
		})
	}
}
