package chineseinla

import (
	"strings"
	"testing"
)

func TestParsePublishedTopics(t *testing.T) {
	t.Parallel()

	htmlSource := `<!doctype html><html><body>
		<a href="/f/page_viewtopic/t_3138905.html"> 洛杉矶人的时间单位 </a>
		<a href="https://www.chineseinla.com/f/page_viewtopic/t_3138899/sid_example.html">在洛杉矶养成的超能力</a>
		<a href="https://evil.example/f/page_viewtopic/t_999.html">伪造主题</a>
		<a href="/f/page_viewforum/f_23.html">轻松一刻</a>
	</body></html>`

	topics, err := parsePublishedTopics(strings.NewReader(htmlSource))
	if err != nil {
		t.Fatalf("parsePublishedTopics: %v", err)
	}
	if len(topics) != 2 {
		t.Fatalf("topic count = %d, want 2: %#v", len(topics), topics)
	}
	if topics[0].Title != "洛杉矶人的时间单位" || topics[0].URL != BaseURL+"/f/page_viewtopic/t_3138905.html" {
		t.Fatalf("unexpected first topic: %#v", topics[0])
	}
	if topics[1].URL != BaseURL+"/f/page_viewtopic/t_3138899.html" {
		t.Fatalf("session suffix was not removed: %#v", topics[1])
	}
}

func TestFirstUnseenTopicURL(t *testing.T) {
	t.Parallel()

	topics := []publishedTopic{
		{Title: "洛杉矶人的时间单位", URL: BaseURL + "/f/page_viewtopic/t_2.html"},
		{Title: "洛杉矶人的时间单位", URL: BaseURL + "/f/page_viewtopic/t_1.html"},
	}
	before := topicURLsWithTitle(topics[1:], "洛杉矶人的时间单位")
	if got, want := firstUnseenTopicURL(topics, "洛杉矶人的时间单位", before), topics[0].URL; got != want {
		t.Fatalf("firstUnseenTopicURL = %q, want %q", got, want)
	}
}
