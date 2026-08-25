package chineseinla

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	BaseURL          = "https://www.chineseinla.com"
	HomeURL          = BaseURL + "/"
	LoginURL         = BaseURL + "/f/page_login.html"
	ForumSelectorURL = BaseURL + "/f/page_selectforum.html"
	MyTopicsURL      = BaseURL + "/user/task_myTopic.html"
	defaultCDPPort   = 9223
)

// PostType maps the three post classifications exposed by ChineseInLA.
type PostType string

const (
	PostTypeQuestion   PostType = "question"
	PostTypeClassified PostType = "classified"
	PostTypeOther      PostType = "other"
)

func (p PostType) FormValue() string {
	switch p {
	case PostTypeQuestion:
		return "1"
	case PostTypeClassified:
		return "2"
	default:
		return "0"
	}
}

func (p PostType) Label() string {
	switch p {
	case PostTypeQuestion:
		return "问题征解"
	case PostTypeClassified:
		return "分类信息"
	default:
		return "其他类型"
	}
}

func ParsePostType(value string) (PostType, error) {
	switch PostType(strings.ToLower(strings.TrimSpace(value))) {
	case PostTypeQuestion:
		return PostTypeQuestion, nil
	case PostTypeClassified:
		return PostTypeClassified, nil
	case PostTypeOther:
		return PostTypeOther, nil
	default:
		return "", fmt.Errorf("invalid post type %q; use question, classified, or other", value)
	}
}

type Config struct {
	CDPPort     int
	ProfileDir  string
	StatePath   string
	PreviewPath string
	BrowserBin  string
	Headless    bool
	Timeout     time.Duration
}

func DefaultConfig() (Config, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve user config directory: %w", err)
	}

	root := filepath.Join(configDir, "xiaohongshu-mcp", "chineseinla")
	port := defaultCDPPort
	if raw := strings.TrimSpace(os.Getenv("CHINESEINLA_CDP_PORT")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			return Config{}, fmt.Errorf("CHINESEINLA_CDP_PORT must be between 1 and 65535")
		}
		port = parsed
	}

	profileDir := strings.TrimSpace(os.Getenv("CHINESEINLA_PROFILE_DIR"))
	if profileDir == "" {
		profileDir = filepath.Join(root, "profile")
	}
	statePath := strings.TrimSpace(os.Getenv("CHINESEINLA_STATE_PATH"))
	if statePath == "" {
		statePath = filepath.Join(root, "prepared.json")
	}
	previewPath := strings.TrimSpace(os.Getenv("CHINESEINLA_PREVIEW_PATH"))
	if previewPath == "" {
		previewPath = filepath.Join(root, "prepared-preview.png")
	}
	headless := false
	if raw := strings.TrimSpace(os.Getenv("CHINESEINLA_HEADLESS")); raw != "" {
		parsed, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return Config{}, fmt.Errorf("CHINESEINLA_HEADLESS must be true or false")
		}
		headless = parsed
	}

	return Config{
		CDPPort:     port,
		ProfileDir:  profileDir,
		StatePath:   statePath,
		PreviewPath: previewPath,
		BrowserBin:  strings.TrimSpace(os.Getenv("CHINESEINLA_BROWSER_BIN")),
		Headless:    headless,
		Timeout:     30 * time.Second,
	}, nil
}

type PrepareRequest struct {
	ForumID  int
	PostType PostType
	Title    string
	Body     string
	Tags     []string

	Images    []string
	ImageURLs []string
	SourceURL string
	VideoURLs []string
}

var (
	phoneLikePattern = regexp.MustCompile(`(?i)(?:\+?1[-. ()]*)?(?:\d[-. ()]*){10,}`)
	emailPattern     = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
)

func (r *PrepareRequest) NormalizeAndValidate() ([]string, error) {
	r.Title = strings.TrimSpace(r.Title)
	r.Body = strings.TrimSpace(r.Body)
	r.Tags = normalizeList(r.Tags)
	r.Images = normalizeList(r.Images)
	r.ImageURLs = normalizeList(r.ImageURLs)
	r.VideoURLs = normalizeList(r.VideoURLs)
	r.SourceURL = strings.TrimSpace(r.SourceURL)

	if r.ForumID <= 0 {
		return nil, errors.New("forum ID must be greater than zero")
	}
	if _, err := ParsePostType(string(r.PostType)); err != nil {
		return nil, err
	}
	if r.Title == "" {
		return nil, errors.New("title is required")
	}
	if r.Body == "" {
		return nil, errors.New("body is required")
	}
	if emailPattern.MatchString(r.Title) || phoneLikePattern.MatchString(r.Title) {
		return nil, errors.New("ChineseInLA rules do not allow phone numbers or email addresses in the title")
	}
	if r.SourceURL != "" {
		if err := validateHTTPURL(r.SourceURL); err != nil {
			return nil, fmt.Errorf("invalid source URL: %w", err)
		}
	}
	for _, raw := range append(append([]string{}, r.ImageURLs...), r.VideoURLs...) {
		if err := validateHTTPURL(raw); err != nil {
			return nil, fmt.Errorf("invalid media URL %q: %w", raw, err)
		}
	}

	var warnings []string
	length := len([]rune(r.Title))
	if length < 8 || length > 15 {
		warnings = append(warnings, fmt.Sprintf("ChineseInLA recommends an 8–15 character title; current title has %d characters", length))
	}
	return warnings, nil
}

func (r PrepareRequest) FinalBody() string {
	body := strings.TrimSpace(r.Body)
	if r.SourceURL != "" && !strings.Contains(body, r.SourceURL) {
		body += "\n\n来源：" + r.SourceURL
	}
	if len(r.VideoURLs) > 0 {
		var missing []string
		for _, raw := range r.VideoURLs {
			if !strings.Contains(body, raw) {
				missing = append(missing, raw)
			}
		}
		if len(missing) > 0 {
			body += "\n\n相关视频：\n" + strings.Join(missing, "\n")
		}
	}
	return body
}

func validateHTTPURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("only http and https URLs are supported")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("URL must contain a host and no credentials")
	}
	return nil
}

func normalizeList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}
