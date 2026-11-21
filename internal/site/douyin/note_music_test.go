package douyin

import (
	"encoding/json"
	"testing"
)

// A note whose music.play_url has no address takes its music from the
// top-level video.play_addr, which on a note carries the background music
// (dev 2026-11-20: 16 notes were partial with 配乐无可用地址). A note
// with play_url keeps it; a note without a music node gains none.
func TestNoteMusicFallsBackToTopLevelPlayAddr(t *testing.T) {
	parse := func(raw string) *WorkDetail {
		t.Helper()
		var node rawAwemeNode
		if err := json.Unmarshal([]byte(raw), &node); err != nil {
			t.Fatal(err)
		}
		w, err := parseAwemeWork(&node, StreamSelectOptions{}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	const images = `"images":[{"uri":"img1","width":1080,"height":1440,"url_list":["https://p3-sign.douyinpic.com/img1.webp"]}]`
	const video = `"video":{"play_addr":{"uri":"v0200fg10000synthmusic","url_list":["http://lf9-music-east.douyinstatic.com/obj/ies-music/synthetic.mp3","https://lf9-music-east.douyinstatic.com/obj/ies-music/synthetic.mp3"]}}`

	w := parse(`{"aweme_id":"7600000000000000001","desc":"图文",` + images + `,` + video + `,"music":{"id_str":"7500000000000000001","title":"原声","play_url":{"url_list":[]}}}`)
	if w.Kind != WorkKindNote || w.Music == nil {
		t.Fatalf("kind %q music %+v", w.Kind, w.Music)
	}
	if len(w.Music.PlayURLs) != 1 || w.Music.PlayURLs[0] != "https://lf9-music-east.douyinstatic.com/obj/ies-music/synthetic.mp3" {
		t.Fatalf("music URLs = %v, want the play_addr address over https", w.Music.PlayURLs)
	}

	w = parse(`{"aweme_id":"7600000000000000002","desc":"图文",` + images + `,` + video + `,"music":{"id_str":"7500000000000000002","title":"配乐","play_url":{"url_list":["https://sf5-hl-cdn-tos.douyinstatic.com/obj/ies-music/own.mp3"]}}}`)
	if len(w.Music.PlayURLs) != 1 || w.Music.PlayURLs[0] != "https://sf5-hl-cdn-tos.douyinstatic.com/obj/ies-music/own.mp3" {
		t.Fatalf("music URLs = %v, want music.play_url kept", w.Music.PlayURLs)
	}

	w = parse(`{"aweme_id":"7600000000000000003","desc":"图文",` + images + `,` + video + `}`)
	if w.Music != nil {
		t.Fatalf("a note without a music node gained music %+v", w.Music)
	}
}
