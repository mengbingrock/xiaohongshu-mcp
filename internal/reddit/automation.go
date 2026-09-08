package reddit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

var publishedPostPattern = regexp.MustCompile(`(?i)/r/([A-Za-z0-9_]{2,32})/comments/([A-Za-z0-9]{5,16})`)

func (a *Automation) CheckLogin(ctx context.Context, profileKey string) (LoginStatus, error) {
	status := LoginStatus{ProfileKey: profileKey, Message: "The Reddit browser profile is not logged in."}
	err := a.withBrowser(ctx, profileKey, func(browser *rod.Browser) error {
		page, err := a.openPage(browser, BaseURL+"/")
		if err != nil {
			return err
		}
		defer page.Close()
		username, err := authenticatedUsername(page)
		if err != nil {
			return err
		}
		info, _ := page.Info()
		status.LoggedIn = true
		status.Username = username
		if info != nil {
			status.URL = info.URL
		}
		status.Message = "Reddit login is active in the isolated browser profile."
		return nil
	})
	if errors.Is(err, ErrNotLoggedIn) {
		return status, nil
	}
	return status, err
}

func (a *Automation) ListForums(ctx context.Context, request ListForumsRequest) (ListForumsResult, error) {
	profileKey, err := ValidateProfileKey(request.ProfileKey)
	if err != nil {
		return ListForumsResult{}, err
	}
	query := strings.TrimSpace(request.Query)
	if len(query) > 80 {
		return ListForumsResult{}, errors.New("query must be 80 characters or fewer")
	}
	limit := request.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return ListForumsResult{}, errors.New("limit must be between 1 and 50")
	}
	result := ListForumsResult{Status: "ok", ProfileKey: profileKey, Query: query}
	err = a.withBrowser(ctx, profileKey, func(browser *rod.Browser) error {
		target := BaseURL + "/subreddits/mine/subscriber/"
		if query != "" {
			target = BaseURL + "/subreddits/search?q=" + url.QueryEscape(query)
		}
		page, err := a.openPage(browser, target)
		if err != nil {
			return err
		}
		defer page.Close()
		if _, err := authenticatedUsername(page); err != nil {
			return err
		}
		source, err := page.HTML()
		if err != nil {
			return fmt.Errorf("read Reddit community page: %w", err)
		}
		forums, err := ParseForums(strings.NewReader(source), limit)
		if err != nil {
			return err
		}
		result.Forums = forums
		result.Count = len(forums)
		return nil
	})
	return result, err
}

func (a *Automation) Restrictions(ctx context.Context, request RestrictionsRequest) (RestrictionsResult, error) {
	profileKey, err := ValidateProfileKey(request.ProfileKey)
	if err != nil {
		return RestrictionsResult{}, err
	}
	subreddit, err := NormalizeCommunity(request.Subreddit)
	if err != nil {
		return RestrictionsResult{}, err
	}
	result := RestrictionsResult{
		Status:    "ok",
		Subreddit: "/r/" + subreddit,
		Allow:     []string{"self", "link", "media"},
		Flairs:    []Flair{},
	}
	err = a.withBrowser(ctx, profileKey, func(browser *rod.Browser) error {
		page, err := a.openPage(browser, BaseURL+"/")
		if err != nil {
			return err
		}
		defer page.Close()
		if _, err := authenticatedUsername(page); err != nil {
			return err
		}

		var about struct {
			Data struct {
				SubmissionType string `json:"submission_type"`
				AllowImages    bool   `json:"allow_images"`
				AllowVideos    bool   `json:"allow_videos"`
			} `json:"data"`
		}
		if err := navigateAndDecodeJSON(page, fmt.Sprintf("%s/r/%s/about.json?raw_json=1", BaseURL, subreddit), &about); err != nil {
			return fmt.Errorf("read r/%s posting restrictions: %w", subreddit, err)
		}
		allow := make([]string, 0, 3)
		switch strings.ToLower(about.Data.SubmissionType) {
		case "any", "":
			allow = append(allow, "self", "link")
		case "self":
			allow = append(allow, "self")
		case "link":
			allow = append(allow, "link")
		}
		if about.Data.AllowImages || about.Data.AllowVideos {
			allow = append(allow, "media")
		}
		if len(allow) == 0 {
			allow = append(allow, "self")
		}
		result.Allow = allow

		var flairs []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if err := navigateAndDecodeJSON(page, fmt.Sprintf("%s/r/%s/api/link_flair_v2.json?raw_json=1", BaseURL, subreddit), &flairs); err == nil {
			for _, flair := range flairs {
				name := strings.TrimSpace(flair.Text)
				if name != "" {
					result.Flairs = append(result.Flairs, Flair{ID: flair.ID, Name: name})
				}
			}
		}
		return nil
	})
	return result, err
}

func (a *Automation) ListPosts(ctx context.Context, request ListPostsRequest) (ListPostsResult, error) {
	profileKey, err := ValidateProfileKey(request.ProfileKey)
	if err != nil {
		return ListPostsResult{}, err
	}
	subreddit, err := NormalizeCommunity(request.Subreddit)
	if err != nil {
		return ListPostsResult{}, err
	}
	sort := strings.ToLower(strings.TrimSpace(request.Sort))
	if sort == "" {
		sort = "new"
	}
	if !map[string]bool{"new": true, "hot": true, "top": true, "rising": true}[sort] {
		return ListPostsResult{}, errors.New("sort must be new, hot, top, or rising")
	}
	limit := request.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return ListPostsResult{}, errors.New("limit must be between 1 and 50")
	}
	result := ListPostsResult{Status: "ok", Subreddit: subreddit, Sort: sort}
	err = a.withBrowser(ctx, profileKey, func(browser *rod.Browser) error {
		page, err := a.openPage(browser, fmt.Sprintf("%s/r/%s/%s/", BaseURL, subreddit, sort))
		if err != nil {
			return err
		}
		defer page.Close()
		if _, err := authenticatedUsername(page); err != nil {
			return err
		}
		source, err := page.HTML()
		if err != nil {
			return fmt.Errorf("read Reddit post listing: %w", err)
		}
		posts, err := ParsePosts(strings.NewReader(source), subreddit, limit)
		if err != nil {
			return err
		}
		result.Posts = posts
		result.Count = len(posts)
		return nil
	})
	return result, err
}

func (a *Automation) ReadPost(ctx context.Context, request ReadPostRequest) (ReadPostResult, error) {
	profileKey, err := ValidateProfileKey(request.ProfileKey)
	if err != nil {
		return ReadPostResult{}, err
	}
	subreddit, err := NormalizeCommunity(request.Subreddit)
	if err != nil {
		return ReadPostResult{}, err
	}
	postID, err := NormalizeThingID(request.PostID, "t3")
	if err != nil {
		return ReadPostResult{}, err
	}
	limit := request.CommentLimit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return ReadPostResult{}, errors.New("comment_limit must be between 1 and 100")
	}
	var result ReadPostResult
	err = a.withBrowser(ctx, profileKey, func(browser *rod.Browser) error {
		page, err := a.openPage(browser, postPageURL(subreddit, postID))
		if err != nil {
			return err
		}
		defer page.Close()
		if _, err := authenticatedUsername(page); err != nil {
			return err
		}
		source, err := page.HTML()
		if err != nil {
			return fmt.Errorf("read Reddit post: %w", err)
		}
		result, err = ParsePost(strings.NewReader(source), subreddit, postID, limit)
		return err
	})
	return result, err
}

func (a *Automation) Publish(ctx context.Context, request PublishRequest) (PublishResult, error) {
	if !request.ConfirmPublish {
		return PublishResult{}, errors.New("confirm_publish must be true after the user approves this exact Reddit post")
	}
	profileKey, err := ValidateProfileKey(request.ProfileKey)
	if err != nil {
		return PublishResult{}, err
	}
	subreddit, err := NormalizeCommunity(request.Subreddit)
	if err != nil {
		return PublishResult{}, err
	}
	postType := strings.ToLower(strings.TrimSpace(request.PostType))
	if !map[string]bool{"self": true, "link": true, "media": true}[postType] {
		return PublishResult{}, errors.New("post_type must be self, link, or media")
	}
	title := strings.TrimSpace(request.Title)
	if title == "" || len([]rune(title)) > 300 {
		return PublishResult{}, errors.New("title must contain 1 to 300 characters")
	}
	if postType == "self" && strings.TrimSpace(request.Body) == "" {
		return PublishResult{}, errors.New("body is required for a self post")
	}
	if postType == "link" {
		parsed, parseErr := url.ParseRequestURI(strings.TrimSpace(request.URL))
		if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return PublishResult{}, errors.New("url must be an absolute HTTP or HTTPS URL for a link post")
		}
	}
	if postType == "media" {
		if !filepath.IsAbs(request.MediaPath) {
			return PublishResult{}, errors.New("media_path must be an absolute local path")
		}
		if info, statErr := os.Stat(request.MediaPath); statErr != nil || info.IsDir() {
			return PublishResult{}, errors.New("media_path does not identify a readable file")
		}
	}

	result := PublishResult{Status: "published", Subreddit: subreddit, Title: title}
	err = a.withBrowser(ctx, profileKey, func(browser *rod.Browser) error {
		mode := "url=true"
		if postType == "self" {
			mode = "selftext=true"
		}
		page, err := a.openPage(browser, fmt.Sprintf("%s/r/%s/submit?%s", BaseURL, subreddit, mode))
		if err != nil {
			return err
		}
		defer page.Close()
		if _, err := authenticatedUsername(page); err != nil {
			return err
		}
		titleField, err := firstVisible(page, `textarea[name="title"], input[name="title"]`)
		if err != nil {
			return fmt.Errorf("find Reddit title field: %w", err)
		}
		if err := replaceText(titleField, title); err != nil {
			return fmt.Errorf("fill Reddit title: %w", err)
		}
		switch postType {
		case "self":
			field, findErr := firstVisible(page, `textarea[name="text"]`)
			if findErr != nil {
				return fmt.Errorf("find Reddit body field: %w", findErr)
			}
			if inputErr := replaceText(field, request.Body); inputErr != nil {
				return fmt.Errorf("fill Reddit body: %w", inputErr)
			}
		case "link":
			field, findErr := firstVisible(page, `input[name="url"]`)
			if findErr != nil {
				return fmt.Errorf("find Reddit URL field: %w", findErr)
			}
			if inputErr := replaceText(field, request.URL); inputErr != nil {
				return fmt.Errorf("fill Reddit URL: %w", inputErr)
			}
		case "media":
			field, findErr := firstAttached(page, `input[type="file"]`)
			if findErr != nil {
				return fmt.Errorf("find Reddit media field: %w", findErr)
			}
			if uploadErr := field.SetFiles([]string{request.MediaPath}); uploadErr != nil {
				return fmt.Errorf("upload Reddit media: %w", uploadErr)
			}
		}
		if strings.TrimSpace(request.Flair) != "" {
			_ = selectFlair(page, request.Flair)
		}
		submit, err := firstVisible(page, `button[name="submit"], button[type="submit"]`)
		if err != nil {
			return fmt.Errorf("find Reddit submit button: %w", err)
		}
		if err := submit.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return fmt.Errorf("submit Reddit post: %w", err)
		}
		publishedURL, postID, err := waitForPublishedPost(page, subreddit, 90*time.Second)
		if err != nil {
			return err
		}
		result.URL = publishedURL
		result.PostID = postID
		result.Message = fmt.Sprintf("Published to r/%s.", subreddit)
		return nil
	})
	return result, err
}

func (a *Automation) Comment(ctx context.Context, request CommentRequest) (CommentResult, error) {
	if !request.ConfirmComment {
		return CommentResult{}, errors.New("confirm_comment must be true after the user approves this exact Reddit comment")
	}
	profileKey, err := ValidateProfileKey(request.ProfileKey)
	if err != nil {
		return CommentResult{}, err
	}
	subreddit, err := NormalizeCommunity(request.Subreddit)
	if err != nil {
		return CommentResult{}, err
	}
	postID, err := NormalizeThingID(request.PostID, "t3")
	if err != nil {
		return CommentResult{}, err
	}
	body := strings.TrimSpace(request.Body)
	if body == "" || len([]rune(body)) > 10_000 {
		return CommentResult{}, errors.New("body must contain 1 to 10000 characters")
	}
	result := CommentResult{Status: "published", PostID: postID}
	err = a.withBrowser(ctx, profileKey, func(browser *rod.Browser) error {
		page, err := a.openPage(browser, postPageURL(subreddit, postID))
		if err != nil {
			return err
		}
		defer page.Close()
		username, err := authenticatedUsername(page)
		if err != nil {
			return err
		}
		if existing, ok := findExistingComment(page, username, body); ok {
			result.Status = "already_exists"
			result.CommentID = existing.ID
			result.URL = existing.URL
			result.Message = "The identical Reddit comment already exists; it was not submitted again."
			return nil
		}
		textarea, err := firstVisible(page, `form.usertext textarea[name="text"]`)
		if err != nil {
			return fmt.Errorf("find Reddit comment field: %w", err)
		}
		if err := replaceText(textarea, body); err != nil {
			return fmt.Errorf("fill Reddit comment: %w", err)
		}
		submit, err := firstVisible(page, `form.usertext button[type="submit"], form.usertext button.save`)
		if err != nil {
			return fmt.Errorf("find Reddit comment button: %w", err)
		}
		if err := submit.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return fmt.Errorf("submit Reddit comment: %w", err)
		}
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if comment, ok := findExistingComment(page, username, body); ok {
				result.CommentID = comment.ID
				result.URL = comment.URL
				result.Message = "Reddit comment published."
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return errors.New("Reddit did not confirm the comment before the timeout")
	})
	return result, err
}

func postPageURL(subreddit, postID string) string {
	return fmt.Sprintf("%s/r/%s/comments/%s/", BaseURL, subreddit, strings.TrimPrefix(postID, "t3_"))
}

func replaceText(element *rod.Element, value string) error {
	if err := element.SelectAllText(); err != nil {
		return err
	}
	return element.Input(value)
}

func navigateAndDecodeJSON(page *rod.Page, targetURL string, destination any) error {
	if err := page.Navigate(targetURL); err != nil {
		return err
	}
	if err := page.Timeout(30 * time.Second).WaitLoad(); err != nil {
		return err
	}
	body, err := page.Timeout(10 * time.Second).Element("body")
	if err != nil {
		return err
	}
	raw, err := body.Text()
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), destination); err != nil {
		return fmt.Errorf("decode Reddit JSON response: %w", err)
	}
	return nil
}

func firstVisible(page *rod.Page, selector string) (*rod.Element, error) {
	elements, err := page.Timeout(30 * time.Second).Elements(selector)
	if err != nil {
		return nil, err
	}
	for _, element := range elements {
		visible, visibleErr := element.Visible()
		if visibleErr == nil && visible {
			return element, nil
		}
	}
	return nil, fmt.Errorf("no visible element matches %s", selector)
}

func firstAttached(page *rod.Page, selector string) (*rod.Element, error) {
	elements, err := page.Timeout(30 * time.Second).Elements(selector)
	if err != nil {
		return nil, err
	}
	if len(elements) == 0 {
		return nil, fmt.Errorf("no element matches %s", selector)
	}
	return elements[0], nil
}

func selectFlair(page *rod.Page, flair string) error {
	button, err := firstVisible(page, `.flairselectbtn`)
	if err != nil {
		return nil
	}
	if err := button.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	rows, err := page.Timeout(10 * time.Second).Elements(`.flairselector .flairrow`)
	if err != nil {
		return err
	}
	for _, row := range rows {
		text, _ := row.Text()
		if strings.EqualFold(cleanText(text), cleanText(flair)) || strings.Contains(strings.ToLower(text), strings.ToLower(flair)) {
			if err := row.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return err
			}
			apply, findErr := firstVisible(page, `.flairselector button[type="submit"]`)
			if findErr == nil {
				return apply.Click(proto.InputMouseButtonLeft, 1)
			}
			return nil
		}
	}
	return fmt.Errorf("Reddit flair %q was not found", flair)
}

func waitForPublishedPost(page *rod.Page, subreddit string, timeout time.Duration) (string, string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := page.Info()
		if err == nil && info != nil {
			match := publishedPostPattern.FindStringSubmatch(info.URL)
			if len(match) == 3 && strings.EqualFold(match[1], subreddit) {
				return canonicalRedditURL(info.URL), "t3_" + strings.ToLower(match[2]), nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "", "", fmt.Errorf("Reddit did not confirm the post to r/%s before the timeout", subreddit)
}

func findExistingComment(page *rod.Page, username, body string) (Comment, bool) {
	source, err := page.HTML()
	if err != nil {
		return Comment{}, false
	}
	result, err := ParsePost(strings.NewReader(source), "unknown", "", 100)
	if err != nil {
		return Comment{}, false
	}
	wanted := cleanText(body)
	for _, comment := range result.Comments {
		if strings.EqualFold(comment.Author, username) && cleanText(comment.Body) == wanted {
			return comment, true
		}
	}
	return Comment{}, false
}
