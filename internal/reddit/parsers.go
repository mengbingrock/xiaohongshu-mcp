package reddit

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var (
	subredditPathPattern = regexp.MustCompile(`(?i)^/r/([A-Za-z0-9_]{2,32})/?$`)
	postPathPattern      = regexp.MustCompile(`(?i)^/r/([A-Za-z0-9_]{2,32})/comments/([A-Za-z0-9]{5,16})(?:/[^/?#]+){0,3}/?$`)
	numberPattern        = regexp.MustCompile(`[\d,]+`)
)

func ParseForums(reader io.Reader, limit int) ([]Forum, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("parse Reddit community page: %w", err)
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	containers := append(elementsByClass(doc, "search-result-subreddit"), elementsByClass(doc, "subreddit")...)
	seen := make(map[string]struct{})
	forums := make([]Forum, 0, limit)
	for _, container := range containers {
		link := firstDescendant(container, func(node *html.Node) bool {
			return node.Data == "a" && subredditNameFromHref(attribute(node, "href")) != ""
		})
		if link == nil {
			continue
		}
		name := subredditNameFromHref(attribute(link, "href"))
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		title := cleanText(nodeText(link))
		if strings.EqualFold(strings.TrimPrefix(title, "r/"), name) {
			title = ""
		}
		description := cleanText(nodeText(firstByClass(container, "search-result-body")))
		if description == "" {
			description = cleanText(nodeText(firstByClass(container, "description")))
		}
		subscribers := parseNumber(nodeText(firstByClass(container, "search-subscribers")))
		forums = append(forums, Forum{
			Name: name, Title: title, URL: "https://www.reddit.com/r/" + name + "/",
			Description: description, Subscribers: subscribers,
			Subscribed: hasClass(container, "subscriber") || hasClass(container, "subscribed"),
		})
		if len(forums) >= limit {
			break
		}
	}
	if len(forums) == 0 {
		return nil, errorsSchema("community")
	}
	return forums, nil
}

func ParsePosts(reader io.Reader, subreddit string, limit int) ([]Post, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("parse Reddit post listing: %w", err)
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	posts := make([]Post, 0, limit)
	for _, row := range elementsByClass(doc, "thing") {
		fullname := strings.TrimSpace(attribute(row, "data-fullname"))
		if !strings.HasPrefix(fullname, "t3_") || hasClass(row, "promoted") {
			continue
		}
		titleLink := firstDescendant(row, func(node *html.Node) bool {
			return node.Data == "a" && hasClass(node, "title")
		})
		commentsLink := firstDescendant(row, func(node *html.Node) bool {
			return node.Data == "a" && hasClass(node, "comments")
		})
		if titleLink == nil || commentsLink == nil {
			continue
		}
		postURL := canonicalRedditURL(attribute(commentsLink, "href"))
		if postURL == "" {
			continue
		}
		body := cleanText(nodeText(firstDescendant(row, func(node *html.Node) bool {
			return node.Data == "div" && hasClass(node, "md")
		})))
		timeNode := firstDescendant(row, func(node *html.Node) bool { return node.Data == "time" })
		published := ""
		if timeNode != nil {
			published = strings.TrimSpace(attribute(timeNode, "datetime"))
		}
		outbound := canonicalOutboundURL(attribute(titleLink, "href"))
		if outbound == postURL {
			outbound = ""
		}
		posts = append(posts, Post{
			ID: fullname, Subreddit: subreddit, Title: cleanText(nodeText(titleLink)),
			Author: strings.TrimSpace(attribute(row, "data-author")), URL: postURL,
			OutboundURL: outbound, Body: body, Score: rowScore(row),
			CommentCount: parseNumber(nodeText(commentsLink)), PublishedAt: published,
			Stickied: hasClass(row, "stickied"),
		})
		if len(posts) >= limit {
			break
		}
	}
	if len(posts) == 0 {
		return nil, errorsSchema("post listing")
	}
	return posts, nil
}

func ParsePost(reader io.Reader, subreddit, expectedPostID string, commentLimit int) (ReadPostResult, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return ReadPostResult{}, fmt.Errorf("parse Reddit post: %w", err)
	}
	if commentLimit <= 0 || commentLimit > 100 {
		commentLimit = 50
	}
	var postRow *html.Node
	for _, row := range elementsByClass(doc, "thing") {
		if strings.HasPrefix(attribute(row, "data-fullname"), "t3_") {
			postRow = row
			break
		}
	}
	if postRow == nil {
		return ReadPostResult{}, errorsSchema("post")
	}
	fullname := strings.TrimSpace(attribute(postRow, "data-fullname"))
	if expectedPostID != "" && !strings.EqualFold(fullname, expectedPostID) {
		return ReadPostResult{}, fmt.Errorf("Reddit returned post %s instead of %s", fullname, expectedPostID)
	}
	titleLink := firstDescendant(postRow, func(node *html.Node) bool { return node.Data == "a" && hasClass(node, "title") })
	postURL := fmt.Sprintf("https://www.reddit.com/r/%s/comments/%s/", subreddit, strings.TrimPrefix(fullname, "t3_"))
	post := Post{
		ID: fullname, Subreddit: subreddit, Title: cleanText(nodeText(titleLink)),
		Author: strings.TrimSpace(attribute(postRow, "data-author")), URL: postURL,
		Body:  cleanText(nodeText(firstDescendant(postRow, func(node *html.Node) bool { return node.Data == "div" && hasClass(node, "md") }))),
		Score: rowScore(postRow), Stickied: hasClass(postRow, "stickied"),
	}
	comments := make([]Comment, 0, commentLimit)
	for _, row := range elementsByClass(doc, "comment") {
		fullname := strings.TrimSpace(attribute(row, "data-fullname"))
		if !strings.HasPrefix(fullname, "t1_") {
			continue
		}
		entry := firstByClass(row, "entry")
		if entry == nil {
			continue
		}
		body := cleanText(nodeText(firstDescendant(entry, func(node *html.Node) bool { return node.Data == "div" && hasClass(node, "md") })))
		if body == "" {
			continue
		}
		permalink := firstDescendant(entry, func(node *html.Node) bool { return node.Data == "a" && hasClass(node, "bylink") })
		timeNode := firstDescendant(entry, func(node *html.Node) bool { return node.Data == "time" })
		published := ""
		if timeNode != nil {
			published = strings.TrimSpace(attribute(timeNode, "datetime"))
		}
		comments = append(comments, Comment{
			ID: fullname, ParentID: strings.TrimSpace(attribute(row, "data-parent-fullname")),
			Author: strings.TrimSpace(attribute(row, "data-author")), Body: body,
			Score: rowScore(entry), PublishedAt: published,
			URL: canonicalRedditURL(attribute(permalink, "href")),
		})
		if len(comments) >= commentLimit {
			break
		}
	}
	post.CommentCount = len(comments)
	return ReadPostResult{Status: "ok", Post: post, Comments: comments}, nil
}

func errorsSchema(kind string) error {
	return fmt.Errorf("no Reddit %s elements found; Reddit may have changed its page schema", kind)
}

func subredditNameFromHref(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	match := subredditPathPattern.FindStringSubmatch(parsed.Path)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func canonicalRedditURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if !parsed.IsAbs() {
		base, _ := url.Parse(BaseURL)
		parsed = base.ResolveReference(parsed)
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "reddit.com" && !strings.HasSuffix(host, ".reddit.com") {
		return ""
	}
	match := postPathPattern.FindStringSubmatch(parsed.Path)
	if len(match) != 3 {
		return ""
	}
	return "https://www.reddit.com" + parsed.EscapedPath()
}

func canonicalOutboundURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return parsed.String()
}

func rowScore(root *html.Node) int {
	node := firstDescendant(root, func(node *html.Node) bool { return hasClass(node, "score") })
	if node == nil {
		return 0
	}
	if title := attribute(node, "title"); title != "" {
		return parseNumber(title)
	}
	return parseNumber(nodeText(node))
}

func parseNumber(value string) int {
	match := numberPattern.FindString(value)
	match = strings.ReplaceAll(match, ",", "")
	parsed, _ := strconv.Atoi(match)
	return parsed
}

func cleanText(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(value, "\u00a0", " ")), " ")
}

func attribute(node *html.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, item := range node.Attr {
		if item.Key == name {
			return item.Val
		}
	}
	return ""
}

func hasClass(node *html.Node, className string) bool {
	if node == nil {
		return false
	}
	for _, current := range strings.Fields(attribute(node, "class")) {
		if current == className {
			return true
		}
	}
	return false
}

func elementsByClass(root *html.Node, className string) []*html.Node {
	matches := make([]*html.Node, 0)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && hasClass(node, className) {
			matches = append(matches, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return matches
}

func firstByClass(root *html.Node, className string) *html.Node {
	return firstDescendant(root, func(node *html.Node) bool { return hasClass(node, className) })
}

func firstDescendant(root *html.Node, matches func(*html.Node) bool) *html.Node {
	if root == nil {
		return nil
	}
	if matches(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if result := firstDescendant(child, matches); result != nil {
			return result
		}
	}
	return nil
}

func nodeText(root *html.Node) string {
	if root == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			builder.WriteString(node.Data)
			builder.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return builder.String()
}
