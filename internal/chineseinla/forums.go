package chineseinla

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var (
	forumLinkPattern = regexp.MustCompile(`/f/page_pppping/mode_newtopic/f_(\d+)\.html`)
	groupLinkPattern = regexp.MustCompile(`/f/c_(\d+)(?:/[^?#]*)?\.html(?:[?#].*)?$`)
	lineBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>`)
)

type Forum struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	GroupID     int    `json:"group_id,omitempty"`
	Group       string `json:"group,omitempty"`
	Description string `json:"description,omitempty"`
	Restricted  bool   `json:"restricted"`
}

func ParseForums(reader io.Reader) ([]Forum, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("parse forum catalog: %w", err)
	}

	seen := make(map[int]struct{})
	forums := make([]Forum, 0, 64)
	appendForums := func(root *html.Node, groupID int, group string) {
		walkLinks(root, func(node *html.Node) {
			href := attr(node, "href")
			match := forumLinkPattern.FindStringSubmatch(href)
			name := strings.TrimSpace(nodeText(node))
			if len(match) != 2 || name == "" {
				return
			}
			id, parseErr := strconv.Atoi(match[1])
			if parseErr != nil {
				return
			}
			if _, exists := seen[id]; exists {
				return
			}
			description := nearbyDescription(node)
			forums = append(forums, Forum{
				ID:          id,
				Name:        name,
				GroupID:     groupID,
				Group:       group,
				Description: description,
				Restricted:  group == "" || looksRestricted(name+" "+description),
			})
			seen[id] = struct{}{}
		})
	}

	groupContainers := elementsByClass(doc, "forum_group_select")
	if len(groupContainers) > 0 {
		for _, container := range groupContainers {
			groupID, group := forumGroup(container)
			appendForums(container, groupID, group)
		}
	} else {
		currentGroupID := 0
		currentGroup := ""
		walkLinks(doc, func(node *html.Node) {
			href := attr(node, "href")
			if match := groupLinkPattern.FindStringSubmatch(href); len(match) == 2 {
				currentGroupID, _ = strconv.Atoi(match[1])
				currentGroup = strings.TrimSpace(nodeText(node))
				return
			}
			if forumLinkPattern.MatchString(href) {
				appendForums(node, currentGroupID, currentGroup)
			}
		})
	}

	if len(forums) == 0 {
		return nil, fmt.Errorf("no forums found; ChineseInLA may have changed its category page")
	}
	return forums, nil
}

func walkLinks(root *html.Node, visit func(*html.Node)) {
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			visit(node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
}

func elementsByClass(root *html.Node, className string) []*html.Node {
	var matches []*html.Node
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

func hasClass(node *html.Node, className string) bool {
	for _, current := range strings.Fields(attr(node, "class")) {
		if current == className {
			return true
		}
	}
	return false
}

func forumGroup(container *html.Node) (int, string) {
	groupID := 0
	group := ""
	walkLinks(container, func(node *html.Node) {
		if groupID != 0 {
			return
		}
		match := groupLinkPattern.FindStringSubmatch(attr(node, "href"))
		if len(match) != 2 {
			return
		}
		groupID, _ = strconv.Atoi(match[1])
		group = strings.TrimSpace(nodeText(node))
	})
	return groupID, group
}

func attr(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}
	return ""
}

func nodeText(node *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(builder.String()), " ")
}

func nearbyDescription(anchor *html.Node) string {
	for container, depth := anchor.Parent, 0; container != nil && depth < 4; container, depth = container.Parent, depth+1 {
		if description := firstImageDescription(container); description != "" {
			return description
		}
		if hasClass(container, "forum_group_select") {
			break
		}
	}
	return ""
}

func firstImageDescription(root *html.Node) string {
	var description string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if description != "" {
			return
		}
		if node.Type == html.ElementNode && node.Data == "img" {
			description = normalizeDescription(attr(node, "title"))
			if description == "" {
				description = normalizeDescription(attr(node, "alt"))
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return description
}

func normalizeDescription(value string) string {
	value = lineBreakPattern.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}

func looksRestricted(value string) bool {
	lower := strings.ToLower(value)
	markers := []string{
		"仅限", "专栏", "指定", "合作商家", "认证商家", "商家专用", "赞助",
		"sponsor", "official only", "authorized only",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
