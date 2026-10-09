package podcast

import (
	"testing"

	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/extensions"
)

func itemWithTranscripts(transcripts ...map[string]string) *gofeed.Item {
	var exts []ext.Extension
	for _, attrs := range transcripts {
		exts = append(exts, ext.Extension{Name: "transcript", Attrs: attrs})
	}
	return &gofeed.Item{
		Extensions: ext.Extensions{
			"podcast": {"transcript": exts},
		},
	}
}

func TestPickTranscript_NoExtensionsReturnsEmpty(t *testing.T) {
	item := &gofeed.Item{}
	url, typ := pickTranscript(item)
	if url != "" || typ != "" {
		t.Errorf("expected empty, got url=%q type=%q", url, typ)
	}
}

func TestPickTranscript_NoTranscriptTagReturnsEmpty(t *testing.T) {
	item := &gofeed.Item{Extensions: ext.Extensions{"podcast": {}}}
	url, typ := pickTranscript(item)
	if url != "" || typ != "" {
		t.Errorf("expected empty, got url=%q type=%q", url, typ)
	}
}

func TestPickTranscript_PrefersPlainTextOverVTT(t *testing.T) {
	item := itemWithTranscripts(
		map[string]string{"url": "https://x/ep.vtt", "type": "text/vtt"},
		map[string]string{"url": "https://x/ep.txt", "type": "text/plain"},
	)
	url, typ := pickTranscript(item)
	if typ != "text/plain" || url != "https://x/ep.txt" {
		t.Errorf("got url=%q type=%q, want the text/plain one", url, typ)
	}
}

func TestPickTranscript_PrefersVTTOverSRT(t *testing.T) {
	item := itemWithTranscripts(
		map[string]string{"url": "https://x/ep.srt", "type": "application/srt"},
		map[string]string{"url": "https://x/ep.vtt", "type": "text/vtt"},
	)
	url, typ := pickTranscript(item)
	if typ != "text/vtt" || url != "https://x/ep.vtt" {
		t.Errorf("got url=%q type=%q, want the text/vtt one", url, typ)
	}
}

// TestPickTranscript_FallsBackToFirstWithURLWhenNoPreferredType checks an
// unrecognized MIME type (e.g. application/json chapters-style transcript)
// still gets picked rather than returning nothing at all.
func TestPickTranscript_FallsBackToFirstWithURLWhenNoPreferredType(t *testing.T) {
	item := itemWithTranscripts(
		map[string]string{"url": "https://x/ep.json", "type": "application/json"},
	)
	url, typ := pickTranscript(item)
	if url != "https://x/ep.json" || typ != "application/json" {
		t.Errorf("got url=%q type=%q, want the fallback json one", url, typ)
	}
}

func TestPickTranscript_SkipsEntriesWithoutURL(t *testing.T) {
	item := itemWithTranscripts(
		map[string]string{"type": "text/plain"}, // no url — must be skipped
		map[string]string{"url": "https://x/ep.vtt", "type": "text/vtt"},
	)
	url, typ := pickTranscript(item)
	if url != "https://x/ep.vtt" || typ != "text/vtt" {
		t.Errorf("got url=%q type=%q, want the entry that actually has a url", url, typ)
	}
}
