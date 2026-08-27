package chineseinla

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-rod/rod"
	"golang.org/x/net/html"
)

const (
	forumPostsPerPage = 15
	topicPostsPerPage = 10
	maxReaderPage     = 100
)

var (
	topicLinkPattern = regexp.MustCompile(`/f/page_viewtopic/t_(\d+)(?:/[^?#]*)?\.html(?:[?#].*)?$`)
	userLinkPattern  = regexp.MustCompile(`/user/id_(\d+)(?:/[^?#]*)?\.html(?:[?#].*)?$`)
	postLinkPattern  = regexp.MustCompile(`/f/page_pppping/mode_quote/p_(\d+)\.html`)
	forumViewPattern = regexp.MustCompile(`/f/page_viewforum/f_(\d+)\.html`)
)

type ListPostsRequest struct {
	CategoryID int `json:"category_id"`
	ForumID    int `json:"forum_id"`
	Page       int `json:"page,omitempty"`
	Limit      int `json:"limit,omitempty"`
}

type ForumPost struct {
	TopicID     int    `json:"topic_id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	AuthorID    int    `json:"author_id,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	ReplyCount  int    `json:"reply_count"`
	ViewCount   int    `json:"view_count"`
	HasImages   bool   `json:"has_images"`
	Highlighted bool   `json:"highlighted"`
}

type ListPostsResult struct {
	Status     string      `json:"status"`
	CategoryID int         `json:"category_id"`
	Forum      Forum       `json:"forum"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
	HasNext    bool        `json:"has_next"`
	Posts      []ForumPost `json:"posts"`
}

type ReadPostRequest struct {
	CategoryID int `json:"category_id"`
	ForumID    int `json:"forum_id"`
	TopicID    int `json:"topic_id"`
	Page       int `json:"page,omitempty"`
	Limit      int `json:"limit,omitempty"`
}

type TopicMessage struct {
	PostID      int      `json:"post_id,omitempty"`
	Floor       int      `json:"floor"`
	Author      string   `json:"author,omitempty"`
	AuthorID    int      `json:"author_id,omitempty"`
	PublishedAt string   `json:"published_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	Body        string   `json:"body"`
	ImageURLs   []string `json:"image_urls,omitempty"`
}

type ReadPostResult struct {
	Status     string         `json:"status"`
	CategoryID int            `json:"category_id"`
	Forum      Forum          `json:"forum"`
	TopicID    int            `json:"topic_id"`
	URL        string         `json:"url"`
	Title      string         `json:"title"`
	Page       int            `json:"page"`
	PageSize   int            `json:"page_size"`
	HasNext    bool           `json:"has_next"`
	Messages   []TopicMessage `json:"messages"`
}

func (a *Automation) ListPosts(ctx context.Context, request ListPostsRequest) (ListPostsResult, error) {
	pageNumber, limit, err := validateReaderRequest(request.CategoryID, request.ForumID, request.Page, request.Limit, forumPostsPerPage)
	if err != nil {
		return ListPostsResult{}, err
	}
	browser, err := a.connect(ctx)
	if err != nil {
		return ListPostsResult{}, err
	}
	forum, err := a.validatedForum(browser, request.CategoryID, request.ForumID)
	if err != nil {
		return ListPostsResult{}, err
	}
	pageURL := fmt.Sprintf("%s/f/page_viewforum/f_%d.html", BaseURL, request.ForumID)
	if pageNumber > 1 {
		pageURL = fmt.Sprintf("%s/f/page_viewforum/f_%d/start_%d.html", BaseURL, request.ForumID, (pageNumber-1)*forumPostsPerPage)
	}
	page, err := openPage(browser, pageURL, a.Config.Timeout)
	if err != nil {
		return ListPostsResult{}, err
	}
	if err := detectHumanVerification(page); err != nil {
		return ListPostsResult{}, err
	}
	source, err := page.HTML()
	if err != nil {
		return ListPostsResult{}, fmt.Errorf("read ChineseInLA forum page: %w", err)
	}
	posts, hasNext, err := ParseForumPosts(strings.NewReader(source), request.ForumID, pageNumber)
	if err != nil {
		return ListPostsResult{}, err
	}
	if len(posts) > limit {
		posts = posts[:limit]
	}
	if err := a.persistCookies(browser); err != nil {
		return ListPostsResult{}, err
	}
	return ListPostsResult{Status: "ok", CategoryID: request.CategoryID, Forum: forum, Page: pageNumber, PageSize: len(posts), HasNext: hasNext, Posts: posts}, nil
}

func (a *Automation) ReadPost(ctx context.Context, request ReadPostRequest) (ReadPostResult, error) {
	pageNumber, limit, err := validateReaderRequest(request.CategoryID, request.ForumID, request.Page, request.Limit, topicPostsPerPage)
	if err != nil {
		return ReadPostResult{}, err
	}
	if request.TopicID <= 0 {
		return ReadPostResult{}, fmt.Errorf("topic_id must be a positive integer")
	}
	browser, err := a.connect(ctx)
	if err != nil {
		return ReadPostResult{}, err
	}
	forum, err := a.validatedForum(browser, request.CategoryID, request.ForumID)
	if err != nil {
		return ReadPostResult{}, err
	}
	pageURL := fmt.Sprintf("%s/f/page_viewtopic/t_%d.html", BaseURL, request.TopicID)
	if pageNumber > 1 {
		pageURL = fmt.Sprintf("%s/f/page_viewtopic/t_%d/start_%d.html", BaseURL, request.TopicID, (pageNumber-1)*topicPostsPerPage)
	}
	page, err := openPage(browser, pageURL, a.Config.Timeout)
	if err != nil {
		return ReadPostResult{}, err
	}
	if err := detectHumanVerification(page); err != nil {
		return ReadPostResult{}, err
	}
	source, err := page.HTML()
	if err != nil {
		return ReadPostResult{}, fmt.Errorf("read ChineseInLA topic page: %w", err)
	}
	result, err := ParseTopic(strings.NewReader(source), request.TopicID, pageNumber)
	if err != nil {
		return ReadPostResult{}, err
	}
	if result.Forum.ID != request.ForumID {
		return ReadPostResult{}, fmt.Errorf("topic %d belongs to forum ID %d, not requested forum ID %d", request.TopicID, result.Forum.ID, request.ForumID)
	}
	if len(result.Messages) > limit {
		result.Messages = result.Messages[:limit]
	}
	result.Status = "ok"
	result.CategoryID = request.CategoryID
	result.Forum = forum
	result.PageSize = len(result.Messages)
	if err := a.persistCookies(browser); err != nil {
		return ReadPostResult{}, err
	}
	return result, nil
}

func (a *Automation) validatedForum(browser *rod.Browser, categoryID, forumID int) (Forum, error) {
	forums, err := a.forumsWithBrowser(browser)
	if err != nil {
		return Forum{}, err
	}
	forum, ok := findForum(forums, forumID)
	if !ok {
		return Forum{}, fmt.Errorf("forum ID %d is not present in the live ChineseInLA catalog", forumID)
	}
	if forum.GroupID != categoryID {
		return Forum{}, fmt.Errorf("forum ID %d belongs to category ID %d, not requested category ID %d", forumID, forum.GroupID, categoryID)
	}
	return forum, nil
}

func validateReaderRequest(categoryID, forumID, page, limit, maximumLimit int) (int, int, error) {
	if categoryID <= 0 {
		return 0, 0, fmt.Errorf("category_id must be a positive integer returned by chineseinla_list_forums")
	}
	if forumID <= 0 {
		return 0, 0, fmt.Errorf("forum_id must be a positive integer returned by chineseinla_list_forums")
	}
	if page == 0 {
		page = 1
	}
	if page < 1 || page > maxReaderPage {
		return 0, 0, fmt.Errorf("page must be between 1 and %d", maxReaderPage)
	}
	if limit == 0 {
		limit = maximumLimit
	}
	if limit < 1 || limit > maximumLimit {
		return 0, 0, fmt.Errorf("limit must be between 1 and %d", maximumLimit)
	}
	return page, limit, nil
}

func ParseForumPosts(reader io.Reader, forumID, page int) ([]ForumPost, bool, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, false, fmt.Errorf("parse ChineseInLA forum page: %w", err)
	}
	posts := make([]ForumPost, 0, forumPostsPerPage)
	for _, row := range elementsByClass(doc, "topic_list_detail") {
		titleLink := firstDescendant(row, func(n *html.Node) bool { return n.Data == "a" && hasClass(n, "title") })
		if titleLink == nil {
			continue
		}
		match := topicLinkPattern.FindStringSubmatch(attr(titleLink, "href"))
		if len(match) != 2 {
			continue
		}
		topicID, _ := strconv.Atoi(match[1])
		authorLink := firstDescendant(firstByClass(row, "author"), func(n *html.Node) bool { return n.Data == "a" })
		authorID := 0
		if authorLink != nil {
			if authorMatch := userLinkPattern.FindStringSubmatch(attr(authorLink, "href")); len(authorMatch) == 2 {
				authorID, _ = strconv.Atoi(authorMatch[1])
			}
		}
		posts = append(posts, ForumPost{
			TopicID: topicID, URL: fmt.Sprintf("%s/f/page_viewtopic/t_%d.html", BaseURL, topicID),
			Title: strings.TrimSpace(nodeText(titleLink)), Author: strings.TrimSpace(nodeText(firstByClass(row, "author"))), AuthorID: authorID,
			UpdatedAt: strings.TrimSpace(nodeText(firstByClass(row, "time"))), ReplyCount: nodeInt(firstByClass(row, "reply_count")), ViewCount: nodeInt(firstByClass(row, "read_count")),
			HasImages: firstByClass(row, "img") != nil, Highlighted: firstByClass(row, "bid") != nil,
		})
	}
	if len(posts) == 0 {
		return nil, false, fmt.Errorf("no topic rows found; ChineseInLA may have changed forum ID %d or its page schema", forumID)
	}
	nextOffset := page * forumPostsPerPage
	hasNext := hasLinkMatching(doc, fmt.Sprintf("/f/page_viewforum/f_%d/start_%d.html", forumID, nextOffset))
	return posts, hasNext, nil
}

func ParseTopic(reader io.Reader, topicID, page int) (ReadPostResult, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return ReadPostResult{}, fmt.Errorf("parse ChineseInLA topic page: %w", err)
	}
	forumID := 0
	forumPath := firstByClass(doc, "forum_path")
	if forumPath == nil {
		return ReadPostResult{}, fmt.Errorf("topic %d did not expose its forum breadcrumb; ChineseInLA may have changed its page schema", topicID)
	}
	walkLinks(forumPath, func(n *html.Node) {
		if forumID != 0 {
			return
		}
		if match := forumViewPattern.FindStringSubmatch(attr(n, "href")); len(match) == 2 {
			forumID, _ = strconv.Atoi(match[1])
		}
	})
	if forumID == 0 {
		return ReadPostResult{}, fmt.Errorf("topic %d forum breadcrumb did not contain a forum ID; it may not exist or ChineseInLA changed its page schema", topicID)
	}
	titleNode := firstByClass(doc, "post_title")
	title := strings.TrimSpace(nodeText(titleNode))
	if title == "" {
		return ReadPostResult{}, fmt.Errorf("topic %d did not expose a title", topicID)
	}
	messages := make([]TopicMessage, 0, topicPostsPerPage)
	for i, row := range elementsByClass(doc, "post_line") {
		bodyNode := firstByClass(row, "real-content")
		if bodyNode == nil {
			continue
		}
		authorLink := firstDescendant(firstByClass(row, "user_name"), func(n *html.Node) bool { return n.Data == "a" })
		authorID := 0
		if authorLink != nil {
			if match := userLinkPattern.FindStringSubmatch(attr(authorLink, "href")); len(match) == 2 {
				authorID, _ = strconv.Atoi(match[1])
			}
		}
		postID := 0
		quoteLink := firstDescendant(firstByClass(row, "post_quote"), func(n *html.Node) bool { return n.Data == "a" })
		if quoteLink != nil {
			if match := postLinkPattern.FindStringSubmatch(attr(quoteLink, "href")); len(match) == 2 {
				postID, _ = strconv.Atoi(match[1])
			}
		}
		publishedAt, updatedAt := parsePostTimesNode(firstByClass(row, "post_time"))
		messages = append(messages, TopicMessage{PostID: postID, Floor: (page-1)*topicPostsPerPage + i + 1, Author: strings.TrimSpace(nodeText(firstByClass(row, "user_name"))), AuthorID: authorID, PublishedAt: publishedAt, UpdatedAt: updatedAt, Body: nodeTextWithBreaks(bodyNode), ImageURLs: descendantImageURLs(bodyNode)})
	}
	if len(messages) == 0 {
		return ReadPostResult{}, fmt.Errorf("no post bodies found for topic %d; ChineseInLA may have changed its page schema", topicID)
	}
	nextOffset := page * topicPostsPerPage
	return ReadPostResult{Forum: Forum{ID: forumID}, TopicID: topicID, URL: fmt.Sprintf("%s/f/page_viewtopic/t_%d.html", BaseURL, topicID), Title: title, Page: page, HasNext: hasLinkMatching(doc, fmt.Sprintf("/f/page_viewtopic/t_%d/start_%d.html", topicID, nextOffset)), Messages: messages}, nil
}

func firstByClass(root *html.Node, className string) *html.Node {
	if root == nil {
		return nil
	}
	return firstDescendant(root, func(n *html.Node) bool { return hasClass(n, className) })
}

func firstDescendant(root *html.Node, match func(*html.Node) bool) *html.Node {
	if root == nil {
		return nil
	}
	if root.Type == html.ElementNode && match(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := firstDescendant(child, match); found != nil {
			return found
		}
	}
	return nil
}

func nodeInt(node *html.Node) int {
	value, _ := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(nodeText(node)), ",", ""))
	return value
}

func hasLinkMatching(root *html.Node, expected string) bool {
	found := false
	walkLinks(root, func(n *html.Node) {
		if attr(n, "href") == expected {
			found = true
		}
	})
	return found
}

func parsePostTimes(value string) (string, string) {
	value = strings.Join(strings.Fields(value), " ")
	published, updated := value, ""
	if index := strings.Index(value, "更新于:"); index >= 0 {
		published, updated = strings.TrimSpace(value[:index]), strings.TrimSpace(strings.TrimPrefix(value[index:], "更新于:"))
	}
	published = strings.TrimSpace(strings.TrimPrefix(published, "发布于:"))
	return published, updated
}

func parsePostTimesNode(root *html.Node) (string, string) {
	published, updated := "", ""
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "span" {
			value := strings.Join(strings.Fields(nodeText(node)), " ")
			switch {
			case strings.HasPrefix(value, "发布于:"):
				published = strings.TrimSpace(strings.TrimPrefix(value, "发布于:"))
			case strings.HasPrefix(value, "更新于:"):
				updated = strings.TrimSpace(strings.TrimPrefix(value, "更新于:"))
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	if root != nil {
		walk(root)
	}
	if published == "" && updated == "" {
		return parsePostTimes(nodeText(root))
	}
	return published, updated
}

func nodeTextWithBreaks(root *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			builder.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "br" {
			builder.WriteByte('\n')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == html.ElementNode && (n.Data == "p" || n.Data == "div" || n.Data == "li") {
			builder.WriteByte('\n')
		}
	}
	walk(root)
	lines := strings.Split(strings.ReplaceAll(builder.String(), "\u00a0", " "), "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			clean = append(clean, line)
		}
	}
	return strings.Join(clean, "\n")
}

func descendantImageURLs(root *html.Node) []string {
	seen := map[string]struct{}{}
	urls := []string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" {
			source := strings.TrimSpace(attr(n, "src"))
			if parsed, err := url.Parse(source); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https" || strings.HasPrefix(source, "/")) {
				if strings.HasPrefix(source, "/") {
					source = BaseURL + source
				}
				if _, ok := seen[source]; !ok {
					seen[source] = struct{}{}
					urls = append(urls, source)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return urls
}
