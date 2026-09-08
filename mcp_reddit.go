package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xpzouying/xiaohongshu-mcp/internal/reddit"
)

type RedditProfileArgs struct {
	ProfileKey string `json:"profile_key" jsonschema:"32-character Postiz Reddit browser profile key"`
}

type RedditListForumsArgs struct {
	ProfileKey string `json:"profile_key" jsonschema:"32-character Postiz Reddit browser profile key"`
	Query      string `json:"query,omitempty" jsonschema:"Optional community search text; when empty returns subscribed communities"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum communities to return, 1 through 50; defaults to 20"`
}

type RedditListPostsArgs struct {
	ProfileKey string `json:"profile_key" jsonschema:"32-character Postiz Reddit browser profile key"`
	Subreddit  string `json:"subreddit" jsonschema:"Community name returned by reddit_list_forums, with or without r/"`
	Sort       string `json:"sort,omitempty" jsonschema:"new, hot, top, or rising; defaults to new"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum posts to return, 1 through 50; defaults to 20"`
}

type RedditRestrictionsArgs struct {
	ProfileKey string `json:"profile_key" jsonschema:"32-character Postiz Reddit browser profile key"`
	Subreddit  string `json:"subreddit" jsonschema:"Community name returned by reddit_list_forums, with or without r/"`
}

type RedditReadPostArgs struct {
	ProfileKey   string `json:"profile_key" jsonschema:"32-character Postiz Reddit browser profile key"`
	Subreddit    string `json:"subreddit" jsonschema:"Community name returned by reddit_list_forums"`
	PostID       string `json:"post_id" jsonschema:"Post ID returned by reddit_list_posts, with or without t3_ prefix"`
	CommentLimit int    `json:"comment_limit,omitempty" jsonschema:"Maximum comments to return, 1 through 100; defaults to 50"`
}

type RedditPublishArgs struct {
	ProfileKey     string `json:"profile_key" jsonschema:"32-character Postiz Reddit browser profile key"`
	Subreddit      string `json:"subreddit" jsonschema:"Target community name returned by reddit_list_forums"`
	PostType       string `json:"post_type" jsonschema:"self, link, or media"`
	Title          string `json:"title" jsonschema:"Reddit post title, 1 through 300 characters"`
	Body           string `json:"body,omitempty" jsonschema:"Required body for self posts"`
	URL            string `json:"url,omitempty" jsonschema:"Required absolute HTTP or HTTPS URL for link posts"`
	MediaPath      string `json:"media_path,omitempty" jsonschema:"Required absolute local file path for media posts"`
	Flair          string `json:"flair,omitempty" jsonschema:"Optional visible flair name"`
	ConfirmPublish bool   `json:"confirm_publish" jsonschema:"Must be true after the user explicitly approves this exact post"`
}

type RedditCommentArgs struct {
	ProfileKey     string `json:"profile_key" jsonschema:"32-character Postiz Reddit browser profile key"`
	Subreddit      string `json:"subreddit" jsonschema:"Community containing the post"`
	PostID         string `json:"post_id" jsonschema:"Post ID returned by reddit_list_posts or reddit_read_post"`
	Body           string `json:"body" jsonschema:"Comment body, 1 through 10000 characters"`
	ConfirmComment bool   `json:"confirm_comment" jsonschema:"Must be true after the user explicitly approves this exact comment"`
}

func registerRedditTools(server *mcp.Server, appServer *AppServer) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "reddit_check_login", Description: "Check the saved Postiz Reddit browser profile without exposing cookies.",
		Annotations: &mcp.ToolAnnotations{Title: "Check Reddit Login", ReadOnlyHint: true},
	}, withPanicRecovery("reddit_check_login", func(ctx context.Context, _ *mcp.CallToolRequest, args RedditProfileArgs) (*mcp.CallToolResult, any, error) {
		return convertToMCPResult(appServer.handleRedditCheckLogin(ctx, args)), nil, nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name: "reddit_list_forums", Description: "List subscribed Reddit communities, or search community names when query is supplied. Call before listing or publishing instead of guessing a subreddit.",
		Annotations: &mcp.ToolAnnotations{Title: "List Reddit Communities", ReadOnlyHint: true},
	}, withPanicRecovery("reddit_list_forums", func(ctx context.Context, _ *mcp.CallToolRequest, args RedditListForumsArgs) (*mcp.CallToolResult, any, error) {
		return convertToMCPResult(appServer.handleRedditListForums(ctx, args)), nil, nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name: "reddit_list_posts", Description: "List posts in one Reddit community through the authenticated browser session. Read-only.",
		Annotations: &mcp.ToolAnnotations{Title: "List Reddit Posts", ReadOnlyHint: true},
	}, withPanicRecovery("reddit_list_posts", func(ctx context.Context, _ *mcp.CallToolRequest, args RedditListPostsArgs) (*mcp.CallToolResult, any, error) {
		return convertToMCPResult(appServer.handleRedditListPosts(ctx, args)), nil, nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name: "reddit_get_restrictions", Description: "Read allowed post types and available flairs for one Reddit community through the authenticated browser session. Read-only.",
		Annotations: &mcp.ToolAnnotations{Title: "Get Reddit Posting Restrictions", ReadOnlyHint: true},
	}, withPanicRecovery("reddit_get_restrictions", func(ctx context.Context, _ *mcp.CallToolRequest, args RedditRestrictionsArgs) (*mcp.CallToolResult, any, error) {
		return convertToMCPResult(appServer.handleRedditRestrictions(ctx, args)), nil, nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name: "reddit_read_post", Description: "Read one Reddit post and its comments by post ID. Read-only.",
		Annotations: &mcp.ToolAnnotations{Title: "Read Reddit Post", ReadOnlyHint: true},
	}, withPanicRecovery("reddit_read_post", func(ctx context.Context, _ *mcp.CallToolRequest, args RedditReadPostArgs) (*mcp.CallToolResult, any, error) {
		return convertToMCPResult(appServer.handleRedditReadPost(ctx, args)), nil, nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name: "reddit_publish_post", Description: "Publish one self, link, or media post through the saved Reddit browser session. Requires exact subreddit and explicit confirm_publish=true.",
		Annotations: &mcp.ToolAnnotations{Title: "Publish Reddit Post", OpenWorldHint: boolPtr(true)},
	}, withPanicRecovery("reddit_publish_post", func(ctx context.Context, _ *mcp.CallToolRequest, args RedditPublishArgs) (*mcp.CallToolResult, any, error) {
		return convertToMCPResult(appServer.handleRedditPublish(ctx, args)), nil, nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name: "reddit_comment_post", Description: "Add one top-level comment to a Reddit post. Requires exact post ID, body, and explicit confirm_comment=true. Identical comments by the same account are not duplicated.",
		Annotations: &mcp.ToolAnnotations{Title: "Comment on Reddit Post", OpenWorldHint: boolPtr(true)},
	}, withPanicRecovery("reddit_comment_post", func(ctx context.Context, _ *mcp.CallToolRequest, args RedditCommentArgs) (*mcp.CallToolResult, any, error) {
		return convertToMCPResult(appServer.handleRedditComment(ctx, args)), nil, nil
	}))
}

func (s *AppServer) handleRedditCheckLogin(ctx context.Context, args RedditProfileArgs) *MCPToolResult {
	return s.withReddit(func(service RedditService) (any, error) { return service.CheckLogin(ctx, args.ProfileKey) })
}

func (s *AppServer) handleRedditListForums(ctx context.Context, args RedditListForumsArgs) *MCPToolResult {
	return s.withReddit(func(service RedditService) (any, error) {
		return service.ListForums(ctx, reddit.ListForumsRequest{ProfileKey: args.ProfileKey, Query: args.Query, Limit: args.Limit})
	})
}

func (s *AppServer) handleRedditListPosts(ctx context.Context, args RedditListPostsArgs) *MCPToolResult {
	return s.withReddit(func(service RedditService) (any, error) {
		return service.ListPosts(ctx, reddit.ListPostsRequest{ProfileKey: args.ProfileKey, Subreddit: args.Subreddit, Sort: args.Sort, Limit: args.Limit})
	})
}

func (s *AppServer) handleRedditRestrictions(ctx context.Context, args RedditRestrictionsArgs) *MCPToolResult {
	return s.withReddit(func(service RedditService) (any, error) {
		return service.Restrictions(ctx, reddit.RestrictionsRequest{ProfileKey: args.ProfileKey, Subreddit: args.Subreddit})
	})
}

func (s *AppServer) handleRedditReadPost(ctx context.Context, args RedditReadPostArgs) *MCPToolResult {
	return s.withReddit(func(service RedditService) (any, error) {
		return service.ReadPost(ctx, reddit.ReadPostRequest{ProfileKey: args.ProfileKey, Subreddit: args.Subreddit, PostID: args.PostID, CommentLimit: args.CommentLimit})
	})
}

func (s *AppServer) handleRedditPublish(ctx context.Context, args RedditPublishArgs) *MCPToolResult {
	if !args.ConfirmPublish {
		return redditRefusal("Refusing to publish without the user's explicit confirmation (confirm_publish=true).")
	}
	return s.withReddit(func(service RedditService) (any, error) {
		return service.Publish(ctx, reddit.PublishRequest{
			ProfileKey: args.ProfileKey, Subreddit: args.Subreddit, PostType: args.PostType,
			Title: args.Title, Body: args.Body, URL: args.URL, MediaPath: args.MediaPath,
			Flair: args.Flair, ConfirmPublish: true,
		})
	})
}

func (s *AppServer) handleRedditComment(ctx context.Context, args RedditCommentArgs) *MCPToolResult {
	if !args.ConfirmComment {
		return redditRefusal("Refusing to comment without the user's explicit confirmation (confirm_comment=true).")
	}
	return s.withReddit(func(service RedditService) (any, error) {
		return service.Comment(ctx, reddit.CommentRequest{
			ProfileKey: args.ProfileKey, Subreddit: args.Subreddit, PostID: args.PostID,
			Body: args.Body, ConfirmComment: true,
		})
	})
}

func (s *AppServer) withReddit(action func(RedditService) (any, error)) *MCPToolResult {
	if s.redditService == nil {
		return redditRefusal("Reddit support is not configured in this MCP process.")
	}
	s.redditMu.Lock()
	defer s.redditMu.Unlock()
	value, err := action(s.redditService)
	if err != nil {
		return redditErrorResult(err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return redditErrorResult(fmt.Errorf("encode Reddit result: %w", err))
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(data)}}}
}

func redditErrorResult(err error) *MCPToolResult {
	action := ""
	switch {
	case errors.Is(err, reddit.ErrNotLoggedIn):
		action = " Reconnect the Reddit Agent channel in Postiz, then retry."
	case errors.Is(err, reddit.ErrBlocked):
		action = " Run Reddit with a visible native Chrome session (reddit-headless=false); headless browsing is commonly blocked."
	}
	return redditRefusal("Reddit operation failed: " + strings.TrimSpace(err.Error()) + action)
}

func redditRefusal(message string) *MCPToolResult {
	return &MCPToolResult{IsError: true, Content: []MCPContent{{Type: "text", Text: message}}}
}
