package weibo

import (
	"strings"
	"testing"
)

func TestNormalizeWeiboJumpURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "protocol-relative weibo user",
			in:   "//weibo.com/u/1000000001",
			want: "https://weibo.com/u/1000000001",
		},
		{
			name: "protocol-relative topic search",
			in:   "//s.weibo.com/weibo?q=%23Go%E8%AF%AD%E8%A8%80%23",
			want: "https://s.weibo.com/weibo?q=%23Go%E8%AF%AD%E8%A8%80%23",
		},
		{
			name: "root-relative mention",
			in:   "/n/合成博主",
			want: "https://weibo.com/n/%E5%90%88%E6%88%90%E5%8D%9A%E4%B8%BB",
		},
		{
			name: "sinaurl redirect unwrap",
			in:   "https://weibo.cn/sinaurl?u=https%3A%2F%2Fexample.com%2Fdocs%2Fguide%3Fpage%3D2",
			want: "https://example.com/docs/guide?page=2",
		},
		{
			name: "sinaurl toasturl unwrap",
			in:   "https://m.weibo.cn/sinaurl?toasturl=https%3A%2F%2Fexample.org%2Fspec",
			want: "https://example.org/spec",
		},
		{
			name: "t.cn http upgraded to https",
			in:   "http://t.cn/A6SYNTH01",
			want: "https://t.cn/A6SYNTH01",
		},
		{
			name: "video.weibo.com show rewritten to canonical tv show",
			in:   "https://video.weibo.com/show?fid=1034:5000000000000099",
			want: "https://weibo.com/tv/show/1034:5000000000000099",
		},
		{
			name: "reject javascript scheme",
			in:   "javascript:alert(1)",
			want: "",
		},
		{
			name: "reject javascript inside sinaurl",
			in:   "https://weibo.cn/sinaurl?u=javascript%3Aalert(1)",
			want: "",
		},
		{
			name: "reject file scheme",
			in:   "file:///etc/passwd",
			want: "",
		},
		{
			name: "sinaweibo pageinfo supertopic rewritten to desktop url",
			in:   "sinaweibo://pageinfo?containerid=100808c9a1f5c18abbfc31cecd32f45a14ddc1&extparam=%E5%90%88%E6%88%90%23tabbar_follow%3D5012",
			want: "https://weibo.com/p/100808c9a1f5c18abbfc31cecd32f45a14ddc1",
		},
		{
			name: "sinaweibo pageinfo with unescaped CJK extparam",
			in:   "sinaweibo://pageinfo?containerid=100808synth_01-ab&extparam=合成超话",
			want: "https://weibo.com/p/100808synth_01-ab",
		},
		{
			name: "sinaweibo pageinfo profile container",
			in:   "sinaweibo://pageinfo?containerid=1005051000000001",
			want: "https://weibo.com/p/1005051000000001",
		},
		{
			name: "sinaweibo userinfo rewritten to desktop profile url",
			in:   "sinaweibo://userinfo?uid=1000000001",
			want: "https://weibo.com/u/1000000001",
		},
		{
			name: "sinaurl wrapping safe sinaweibo pageinfo",
			in:   "https://weibo.cn/sinaurl?u=sinaweibo%3A%2F%2Fpageinfo%3Fcontainerid%3D100808synth01",
			want: "https://weibo.com/p/100808synth01",
		},
		{
			name: "reject unknown sinaweibo custom path",
			in:   "sinaweibo://browser?url=https%3A%2F%2Fexample.com",
			want: "",
		},
		{
			name: "reject sinaweibo detail custom scheme",
			in:   "sinaweibo://detail?mblogid=O00000001",
			want: "",
		},
		{
			name: "reject sinaweibo pageinfo invalid containerid",
			in:   "sinaweibo://pageinfo?containerid=100808../evil",
			want: "",
		},
		{
			name: "reject sinaweibo pageinfo extra path",
			in:   "sinaweibo://pageinfo/extra?containerid=100808synth01",
			want: "",
		},
		{
			name: "reject sinaweibo pageinfo signed marker",
			in:   "sinaweibo://pageinfo?containerid=100808synth01&ssig=syn_secret&Expires=1800000000",
			want: "",
		},
		{
			name: "reject sinaweibo pageinfo credential query",
			in:   "sinaweibo://pageinfo?containerid=100808synth01&SUB=syn_cookie",
			want: "",
		},
		{
			name: "reject sinaweibo pageinfo userinfo credentials",
			in:   "sinaweibo://user:secret@pageinfo?containerid=100808synth01",
			want: "",
		},
		{
			name: "reject sinaweibo userinfo invalid uid",
			in:   "sinaweibo://userinfo?uid=1234",
			want: "",
		},
		{
			name: "reject sinaweibo userinfo leading zero uid",
			in:   "sinaweibo://userinfo?uid=0123456",
			want: "",
		},
		{
			name: "reject userinfo url",
			in:   "https://user:secret@example.com/path",
			want: "",
		},
		{
			name: "reject signed media stream",
			in:   "https://f.video.weibocdn.com/u.mp4?Expires=1800000000&ssig=syn_secret&KID=unistore,video",
			want: "",
		},
		{
			name: "reject livephoto media play endpoint",
			in:   "https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2Fa.mov",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeWeiboJumpURL(tc.in, false); got != tc.want {
				t.Fatalf("NormalizeWeiboJumpURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractWeiboTextAndLinksRichAnchorsAndDuplicateLabels(t *testing.T) {
	rawHTML := `发布归档记录 <a href="//s.weibo.com/weibo?q=%23%E7%B3%BB%E7%BB%9F%E5%B7%A5%E7%A8%8B%23" target="_blank">#系统工程#</a> ` +
		`<a data-url="http://t.cn/A6SUPER1" href="http://weibo.com/p/100808synth01"><span class="surl-text">摄影超话</span></a> ` +
		`一文：<a data-url="http://t.cn/A6DOC1" href="https://weibo.cn/sinaurl?u=https%3A%2F%2Fexample.com%2Ffirst"><span class="url-icon"><img src="https://h5.sinaimg.cn/icon.png"></span><span class="surl-text">网页链接</span></a> ` +
		`二文：<a data-url="http://t.cn/A6DOC2" href="https://weibo.cn/sinaurl?u=https%3A%2F%2Fexample.com%2Fsecond"><span class="surl-text">网页链接</span></a> ` +
		`感谢 <a href="/n/测试作者">@测试作者</a> <a href="/500000001/P03d70010">全文</a>`

	rawText := "发布归档记录 #系统工程# #摄影[超话]# 一文：http://t.cn/A6DOC1 二文：http://t.cn/A6DOC2 感谢 @测试作者"
	mappings := []URLStructItem{
		{ShortURL: "http://t.cn/A6DOC1", LongURL: "https://example.com/first", URLTitle: "网页链接"},
		{ShortURL: "http://t.cn/A6DOC2", LongURL: "https://example.com/second", URLTitle: "网页链接"},
		{ShortURL: "http://t.cn/A6SUPER1", LongURL: "https://weibo.com/p/100808synth01", OriURL: "sinaweibo://pageinfo?containerid=100808synth01", URLTitle: "摄影超话"},
	}

	text, links := extractWeiboTextAndLinks(rawHTML, rawText, mappings, false)
	for _, rawForbidden := range []string{"http://t.cn/", "https://example.com", "[超话]#", "全文"} {
		if strings.Contains(text, rawForbidden) {
			t.Fatalf("clean text still contains raw markup/URL %q: %q", rawForbidden, text)
		}
	}
	wantText := "发布归档记录 #系统工程# 摄影超话 一文：网页链接 二文：网页链接 感谢 @测试作者"
	if text != wantText {
		t.Fatalf("text = %q, want %q", text, wantText)
	}

	if len(links) != 5 {
		t.Fatalf("len(links) = %d, want 5: %+v", len(links), links)
	}
	wantLinks := [][2]string{
		{"#系统工程#", "https://s.weibo.com/weibo?q=%23%E7%B3%BB%E7%BB%9F%E5%B7%A5%E7%A8%8B%23"},
		{"摄影超话", "https://weibo.com/p/100808synth01"},
		{"网页链接", "https://example.com/first"},
		{"网页链接", "https://example.com/second"},
		{"@测试作者", "https://weibo.com/n/%E6%B5%8B%E8%AF%95%E4%BD%9C%E8%80%85"},
	}
	for i, w := range wantLinks {
		if links[i].Label != w[0] || links[i].URL != w[1] {
			t.Fatalf("links[%d] = %+v, want {Label:%q URL:%q}", i, links[i], w[0], w[1])
		}
	}
}

func TestExtractWeiboTextAndLinksSuperTopicOriURLOnly(t *testing.T) {
	const (
		cid    = "100808c9a1f5c18abbfc31cecd32f45a14ddc1"
		oriURL = "sinaweibo://pageinfo?containerid=" + cid + "&extparam=%E5%90%88%E6%88%90%23tabbar_follow%3D5012"
		wantU  = "https://weibo.com/p/" + cid
	)

	cases := []struct {
		name     string
		rawHTML  string
		rawText  string
		mappings []URLStructItem
		wantText string
	}{
		{
			name:    "hash supertopic markup with empty long_url and ori_url sinaweibo",
			rawHTML: "",
			rawText: "#合成[超话]# 今日实拍记录",
			mappings: []URLStructItem{
				{ShortURL: "http://t.cn/A6SUP01", LongURL: "", OriURL: oriURL, URLTitle: "合成超话"},
			},
			wantText: "合成超话 今日实拍记录",
		},
		{
			name:    "plain supertopic title with both long_url and ori_url sinaweibo",
			rawHTML: "",
			rawText: "合成超话 今日实拍记录",
			mappings: []URLStructItem{
				{ShortURL: "", LongURL: oriURL, OriURL: oriURL, URLTitle: "合成超话"},
			},
			wantText: "合成超话 今日实拍记录",
		},
		{
			name:    "t.cn short url resolved through sinaweibo ori_url",
			rawHTML: "",
			rawText: "http://t.cn/A6SUP02 今日实拍记录",
			mappings: []URLStructItem{
				{ShortURL: "http://t.cn/A6SUP02", LongURL: "http://t.cn/A6SUP02", OriURL: oriURL, URLTitle: "合成超话"},
			},
			wantText: "合成超话 今日实拍记录",
		},
		{
			name:     "html anchor directly carrying sinaweibo pageinfo href",
			rawHTML:  `<a href="sinaweibo://pageinfo?containerid=` + cid + `&amp;extparam=合成超话">合成超话</a> 今日实拍记录`,
			rawText:  "",
			wantText: "合成超话 今日实拍记录",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotLinks := extractWeiboTextAndLinks(tc.rawHTML, tc.rawText, tc.mappings, false)
			if gotText != tc.wantText {
				t.Fatalf("text = %q, want %q", gotText, tc.wantText)
			}
			if len(gotLinks) != 1 || gotLinks[0].Label != "合成超话" || gotLinks[0].URL != wantU {
				t.Fatalf("links = %+v, want [{Label:合成超话 URL:%s}]", gotLinks, wantU)
			}
		})
	}
}
