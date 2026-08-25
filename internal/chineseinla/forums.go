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
	groupLinkPattern = regexp.MustCompile(`/f/c_\d+\.html`)
)

type Forum struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Group       string `json:"group,omitempty"`
	Description string `json:"description,omitempty"`
	Restricted  bool   `json:"restricted"`
}

func ParseForums(reader io.Reader) ([]Forum, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("parse forum catalog: %w", err)
	}

	currentGroup := ""
	seen := make(map[int]struct{})
	forums := make([]Forum, 0, 64)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			href := attr(node, "href")
			name := strings.TrimSpace(nodeText(node))
			if name != "" && groupLinkPattern.MatchString(href) {
				currentGroup = name
			}
			if match := forumLinkPattern.FindStringSubmatch(href); len(match) == 2 && name != "" {
				id, parseErr := strconv.Atoi(match[1])
				if parseErr == nil {
					if _, exists := seen[id]; !exists {
						description := nearbyDescription(node)
						forums = append(forums, Forum{
							ID:          id,
							Name:        name,
							Group:       currentGroup,
							Description: description,
							Restricted:  currentGroup == "" || looksRestricted(name+" "+description),
						})
						seen[id] = struct{}{}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	if len(forums) == 0 {
		return nil, fmt.Errorf("no forums found; ChineseInLA may have changed its category page")
	}
	return forums, nil
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
	container := anchor.Parent
	if container == nil {
		return ""
	}
	for current := container; current != nil && current != anchor; current = current.FirstChild {
		if current.Type == html.ElementNode && current.Data == "img" {
			if alt := strings.TrimSpace(attr(current, "alt")); alt != "" {
				return alt
			}
		}
	}
	for sibling := anchor.PrevSibling; sibling != nil; sibling = sibling.PrevSibling {
		if sibling.Type == html.ElementNode && sibling.Data == "img" {
			return strings.TrimSpace(attr(sibling, "alt"))
		}
		if sibling.Type == html.TextNode && strings.TrimSpace(sibling.Data) != "" {
			break
		}
	}
	return ""
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
