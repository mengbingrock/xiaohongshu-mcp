package chineseinla

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var topicPathPattern = regexp.MustCompile(`^/f/page_viewtopic/t_(\d+)(?:\.html|/[^/]+\.html)$`)

type publishedTopic struct {
	Title string
	URL   string
}

func parsePublishedTopics(reader io.Reader) ([]publishedTopic, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("parse published-topic list: %w", err)
	}

	seen := make(map[string]struct{})
	topics := make([]publishedTopic, 0)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			title := normalizeTopicTitle(nodeText(node))
			if title != "" {
				if topicURL := canonicalTopicURL(attr(node, "href")); topicURL != "" {
					if _, exists := seen[topicURL]; !exists {
						topics = append(topics, publishedTopic{Title: title, URL: topicURL})
						seen[topicURL] = struct{}{}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return topics, nil
}

func canonicalTopicURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if !parsed.IsAbs() {
		base, _ := url.Parse(BaseURL)
		parsed = base.ResolveReference(parsed)
	}
	if parsed.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "www.chineseinla.com" && host != "chineseinla.com" {
		return ""
	}
	match := topicPathPattern.FindStringSubmatch(parsed.Path)
	if len(match) != 2 {
		return ""
	}
	return fmt.Sprintf("%s/f/page_viewtopic/t_%s.html", BaseURL, match[1])
}

func normalizeTopicTitle(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func topicURLsWithTitle(topics []publishedTopic, title string) map[string]struct{} {
	title = normalizeTopicTitle(title)
	result := make(map[string]struct{})
	for _, topic := range topics {
		if normalizeTopicTitle(topic.Title) == title {
			result[topic.URL] = struct{}{}
		}
	}
	return result
}

func firstUnseenTopicURL(topics []publishedTopic, title string, before map[string]struct{}) string {
	title = normalizeTopicTitle(title)
	for _, topic := range topics {
		if normalizeTopicTitle(topic.Title) != title {
			continue
		}
		if _, existed := before[topic.URL]; !existed {
			return topic.URL
		}
	}
	return ""
}
