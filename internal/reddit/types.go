package reddit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	BaseURL        = "https://en.reddit.com"
	CookieFileName = "reddit-browser-state.json"
)

var (
	ErrNotLoggedIn = errors.New("not logged in to Reddit")
	ErrBlocked     = errors.New("Reddit blocked the browser session")
	profilePattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	communityName  = regexp.MustCompile(`^[A-Za-z0-9_]{2,32}$`)
	thingIDPattern = regexp.MustCompile(`^(?:t([13])_)?([A-Za-z0-9]{5,16})$`)
)

type Config struct {
	ProfileRoot string
	BrowserBin  string
	Headless    bool
	Timeout     time.Duration
}

func DefaultConfig() (Config, error) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve Reddit configuration directory: %w", err)
	}
	profileRoot := strings.TrimSpace(os.Getenv("REDDIT_PROFILE_ROOT"))
	if profileRoot == "" {
		profileRoot = filepath.Join(configRoot, "xiaohongshu-mcp", "reddit", "profiles")
	}
	headless := false
	if value := strings.TrimSpace(os.Getenv("REDDIT_HEADLESS")); value != "" {
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("REDDIT_HEADLESS must be true or false: %w", parseErr)
		}
		headless = parsed
	}
	return Config{
		ProfileRoot: filepath.Clean(profileRoot),
		BrowserBin:  strings.TrimSpace(os.Getenv("REDDIT_BROWSER_BIN")),
		Headless:    headless,
		Timeout:     60 * time.Second,
	}, nil
}

func ValidateProfileKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !profilePattern.MatchString(value) {
		return "", errors.New("profile_key must be the 32-character identifier returned by the Postiz Reddit login flow")
	}
	return value, nil
}

func NormalizeCommunity(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "/")
	value = strings.TrimPrefix(strings.ToLower(value), "r/")
	if !communityName.MatchString(value) {
		return "", errors.New("subreddit must contain 2 to 32 letters, numbers, or underscores")
	}
	return value, nil
}

func NormalizeThingID(value string, prefix string) (string, error) {
	match := thingIDPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 3 || (prefix != "t1" && prefix != "t3") {
		return "", fmt.Errorf("invalid Reddit %s ID", prefix)
	}
	if match[1] != "" && "t"+match[1] != prefix {
		return "", fmt.Errorf("invalid Reddit %s ID", prefix)
	}
	return prefix + "_" + strings.ToLower(match[2]), nil
}

func (c Config) ProfileDir(profileKey string) (string, error) {
	profileKey, err := ValidateProfileKey(profileKey)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.ProfileRoot, profileKey), nil
}

func (c Config) CookiePath(profileKey string) (string, error) {
	directory, err := c.ProfileDir(profileKey)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, CookieFileName), nil
}

type LoginStatus struct {
	LoggedIn   bool   `json:"logged_in"`
	Username   string `json:"username,omitempty"`
	ProfileKey string `json:"profile_key"`
	URL        string `json:"url,omitempty"`
	Message    string `json:"message"`
}

type Forum struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Subscribers int    `json:"subscribers,omitempty"`
	Subscribed  bool   `json:"subscribed,omitempty"`
}

type ListForumsRequest struct {
	ProfileKey string `json:"profile_key"`
	Query      string `json:"query,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type ListForumsResult struct {
	Status     string  `json:"status"`
	ProfileKey string  `json:"profile_key"`
	Query      string  `json:"query,omitempty"`
	Count      int     `json:"count"`
	Forums     []Forum `json:"forums"`
}

type RestrictionsRequest struct {
	ProfileKey string `json:"profile_key"`
	Subreddit  string `json:"subreddit"`
}

type Flair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RestrictionsResult struct {
	Status          string   `json:"status"`
	Subreddit       string   `json:"subreddit"`
	Allow           []string `json:"allow"`
	IsFlairRequired bool     `json:"is_flair_required"`
	Flairs          []Flair  `json:"flairs"`
}

type Post struct {
	ID           string `json:"id"`
	Subreddit    string `json:"subreddit"`
	Title        string `json:"title"`
	Author       string `json:"author,omitempty"`
	URL          string `json:"url"`
	OutboundURL  string `json:"outbound_url,omitempty"`
	Body         string `json:"body,omitempty"`
	Score        int    `json:"score"`
	CommentCount int    `json:"comment_count"`
	PublishedAt  string `json:"published_at,omitempty"`
	Stickied     bool   `json:"stickied,omitempty"`
}

type ListPostsRequest struct {
	ProfileKey string `json:"profile_key"`
	Subreddit  string `json:"subreddit"`
	Sort       string `json:"sort,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type ListPostsResult struct {
	Status    string `json:"status"`
	Subreddit string `json:"subreddit"`
	Sort      string `json:"sort"`
	Count     int    `json:"count"`
	Posts     []Post `json:"posts"`
}

type Comment struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	Author      string `json:"author,omitempty"`
	Body        string `json:"body"`
	Score       int    `json:"score"`
	PublishedAt string `json:"published_at,omitempty"`
	URL         string `json:"url,omitempty"`
}

type ReadPostRequest struct {
	ProfileKey   string `json:"profile_key"`
	Subreddit    string `json:"subreddit"`
	PostID       string `json:"post_id"`
	CommentLimit int    `json:"comment_limit,omitempty"`
}

type ReadPostResult struct {
	Status   string    `json:"status"`
	Post     Post      `json:"post"`
	Comments []Comment `json:"comments"`
}

type PublishRequest struct {
	ProfileKey     string `json:"profile_key"`
	Subreddit      string `json:"subreddit"`
	PostType       string `json:"post_type"`
	Title          string `json:"title"`
	Body           string `json:"body,omitempty"`
	URL            string `json:"url,omitempty"`
	MediaPath      string `json:"media_path,omitempty"`
	Flair          string `json:"flair,omitempty"`
	ConfirmPublish bool   `json:"confirm_publish"`
}

type PublishResult struct {
	Status    string `json:"status"`
	PostID    string `json:"post_id"`
	URL       string `json:"url"`
	Subreddit string `json:"subreddit"`
	Title     string `json:"title"`
	Message   string `json:"message"`
}

type CommentRequest struct {
	ProfileKey     string `json:"profile_key"`
	Subreddit      string `json:"subreddit"`
	PostID         string `json:"post_id"`
	Body           string `json:"body"`
	ConfirmComment bool   `json:"confirm_comment"`
}

type CommentResult struct {
	Status    string `json:"status"`
	CommentID string `json:"comment_id"`
	URL       string `json:"url"`
	PostID    string `json:"post_id"`
	Message   string `json:"message"`
}
