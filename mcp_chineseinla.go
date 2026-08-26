package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xpzouying/xiaohongshu-mcp/internal/chineseinla"
)

type ChineseInLAPreparePostArgs struct {
	ForumID            int      `json:"forum_id" jsonschema:"ChineseInLA forum ID returned by chineseinla_list_forums"`
	PostType           string   `json:"post_type" jsonschema:"Post classification: question, classified, or other"`
	Title              string   `json:"title" jsonschema:"Post title; ChineseInLA recommends 8–15 Chinese characters and disallows phone numbers or email addresses in titles"`
	Body               string   `json:"body" jsonschema:"Post body"`
	Tags               []string `json:"tags,omitempty" jsonschema:"Optional tags to append in the editor"`
	Images             []string `json:"images,omitempty" jsonschema:"Optional local image paths"`
	ImageURLs          []string `json:"image_urls,omitempty" jsonschema:"Optional public HTTP/HTTPS image URLs to download and upload"`
	SourceURL          string   `json:"source_url,omitempty" jsonschema:"Optional source URL appended as attribution"`
	VideoURLs          []string `json:"video_urls,omitempty" jsonschema:"Optional video URLs preserved as links in the body"`
	ConfirmPreparation bool     `json:"confirm_preparation" jsonschema:"Must be true after the user explicitly confirms the exact forum, post type, title, body, tags, and media may be filled into ChineseInLA"`
}

type ChineseInLAPublishPostArgs struct {
	DraftID        string `json:"draft_id" jsonschema:"Exact draft_id returned by chineseinla_prepare_post"`
	ConfirmPublish bool   `json:"confirm_publish" jsonschema:"Must be true only after the user reviews the visible form or returned headless preview and explicitly confirms publication"`
}

type ChineseInLALoginSessionArgs struct {
	SessionID string `json:"session_id" jsonschema:"Opaque session_id returned by chineseinla_open_login"`
}

type ChineseInLASubmitLoginPasswordArgs struct {
	SessionID string `json:"session_id" jsonschema:"Opaque session_id returned by chineseinla_open_login"`
	Username  string `json:"username" jsonschema:"ChineseInLA username, email address, or phone number"`
	Password  string `json:"password" jsonschema:"ChineseInLA account password; never logged or echoed"`
}

func registerChineseInLATools(server *mcp.Server, appServer *AppServer) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "chineseinla_open_login",
			Description: "Start a five-minute ChineseInLA login session in its isolated browser profile. In headless mode this returns a redacted login-page screenshot and a session_id for password submission.",
			Annotations: &mcp.ToolAnnotations{Title: "Open ChineseInLA Login"},
		},
		withPanicRecovery("chineseinla_open_login", func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleChineseInLAOpenLogin(ctx)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "chineseinla_get_login_session_status",
			Description: "Query a retained ChineseInLA login session and return its latest state plus a redacted screenshot when the page is still open.",
			Annotations: &mcp.ToolAnnotations{Title: "Get ChineseInLA Login Session", ReadOnlyHint: true},
		},
		withPanicRecovery("chineseinla_get_login_session_status", func(ctx context.Context, _ *mcp.CallToolRequest, args ChineseInLALoginSessionArgs) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleChineseInLAGetLoginSession(ctx, args)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "chineseinla_submit_login_password",
			Description: "Submit ChineseInLA credentials to the retained headless login page. The password is not logged or echoed. On a shared or remote server, prefer the AUTH_TOKEN-protected HTTPS endpoint so the password does not pass through a model conversation. CAPTCHA is reported and never bypassed.",
			Annotations: &mcp.ToolAnnotations{Title: "Submit ChineseInLA Password", OpenWorldHint: boolPtr(true)},
		},
		withPanicRecovery("chineseinla_submit_login_password", func(ctx context.Context, _ *mcp.CallToolRequest, args ChineseInLASubmitLoginPasswordArgs) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleChineseInLASubmitLoginPassword(ctx, args)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "chineseinla_check_login",
			Description: "Check whether the isolated ChineseInLA browser profile is logged in.",
			Annotations: &mcp.ToolAnnotations{Title: "Check ChineseInLA Login", ReadOnlyHint: true},
		},
		withPanicRecovery("chineseinla_check_login", func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleChineseInLACheckLogin(ctx)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "chineseinla_list_forums",
			Description: "Read the live ChineseInLA forum catalog and return valid forum IDs. Call this before preparing a post instead of guessing a forum ID.",
			Annotations: &mcp.ToolAnnotations{Title: "List ChineseInLA Forums", ReadOnlyHint: true},
		},
		withPanicRecovery("chineseinla_list_forums", func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleChineseInLAListForums(ctx)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "chineseinla_prepare_post",
			Description: "After the user's first explicit confirmation, fill a ChineseInLA post form without publishing it. " +
				"Returns a draft_id and, in headless mode, a screenshot that must be reviewed before a separate publish call.",
			Annotations: &mcp.ToolAnnotations{Title: "Prepare ChineseInLA Post", DestructiveHint: boolPtr(true)},
		},
		withPanicRecovery("chineseinla_prepare_post", func(ctx context.Context, _ *mcp.CallToolRequest, args ChineseInLAPreparePostArgs) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleChineseInLAPreparePost(ctx, args)), nil, nil
		}),
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "chineseinla_publish_post",
			Description: "Publish exactly the prepared ChineseInLA draft. Call only after the user reviews the prepared form or screenshot and gives a second explicit confirmation; " +
				"confirm_publish must be true and draft_id must match chineseinla_prepare_post.",
			Annotations: &mcp.ToolAnnotations{Title: "Publish ChineseInLA Post", DestructiveHint: boolPtr(true)},
		},
		withPanicRecovery("chineseinla_publish_post", func(ctx context.Context, _ *mcp.CallToolRequest, args ChineseInLAPublishPostArgs) (*mcp.CallToolResult, any, error) {
			return convertToMCPResult(appServer.handleChineseInLAPublishPost(ctx, args)), nil, nil
		}),
	)
}

func (s *AppServer) handleChineseInLAOpenLogin(ctx context.Context) *MCPToolResult {
	if unavailable := s.chineseInLAUnavailable(); unavailable != nil {
		return unavailable
	}
	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()

	result, err := s.chineseInLAService.StartLogin(ctx)
	if err != nil {
		return chineseInLAErrorResult(err)
	}
	return chineseInLALoginSessionResult(result)
}

func (s *AppServer) handleChineseInLAGetLoginSession(ctx context.Context, args ChineseInLALoginSessionArgs) *MCPToolResult {
	if unavailable := s.chineseInLAUnavailable(); unavailable != nil {
		return unavailable
	}
	if strings.TrimSpace(args.SessionID) == "" {
		return chineseInLARefusal("ChineseInLA session_id is required.")
	}
	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()

	result, err := s.chineseInLAService.GetLoginSession(ctx, strings.TrimSpace(args.SessionID))
	if err != nil {
		return chineseInLAErrorResult(err)
	}
	return chineseInLALoginSessionResult(result)
}

func (s *AppServer) handleChineseInLASubmitLoginPassword(ctx context.Context, args ChineseInLASubmitLoginPasswordArgs) *MCPToolResult {
	if unavailable := s.chineseInLAUnavailable(); unavailable != nil {
		return unavailable
	}
	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()

	// Never log or echo args: password is a long-lived credential.
	request := chineseinla.PasswordLoginRequest{
		SessionID: args.SessionID,
		Username:  args.Username,
		Password:  args.Password,
	}
	result, err := s.chineseInLAService.SubmitPasswordLogin(ctx, request)
	request.Password = ""
	args.Password = ""
	if err != nil {
		return chineseInLAErrorResult(err)
	}
	return chineseInLALoginSessionResult(result)
}

func (s *AppServer) handleChineseInLACheckLogin(ctx context.Context) *MCPToolResult {
	if unavailable := s.chineseInLAUnavailable(); unavailable != nil {
		return unavailable
	}
	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()

	result, err := s.chineseInLAService.CheckLogin(ctx)
	if err != nil {
		return chineseInLAErrorResult(err)
	}
	return chineseInLAJSONResult(result)
}

func (s *AppServer) handleChineseInLAListForums(ctx context.Context) *MCPToolResult {
	if unavailable := s.chineseInLAUnavailable(); unavailable != nil {
		return unavailable
	}
	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()

	forums, err := s.chineseInLAService.Forums(ctx)
	if err != nil {
		return chineseInLAErrorResult(err)
	}
	return chineseInLAJSONResult(struct {
		Status string              `json:"status"`
		Count  int                 `json:"count"`
		Forums []chineseinla.Forum `json:"forums"`
	}{Status: "ok", Count: len(forums), Forums: forums})
}

func (s *AppServer) handleChineseInLAPreparePost(ctx context.Context, args ChineseInLAPreparePostArgs) *MCPToolResult {
	if !args.ConfirmPreparation {
		return chineseInLARefusal("Refusing to fill the ChineseInLA form without the user's first explicit confirmation (confirm_preparation=true).")
	}
	if unavailable := s.chineseInLAUnavailable(); unavailable != nil {
		return unavailable
	}
	postType, err := chineseinla.ParsePostType(args.PostType)
	if err != nil {
		return chineseInLAErrorResult(err)
	}

	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()
	result, err := s.chineseInLAService.Prepare(ctx, chineseinla.PrepareRequest{
		ForumID:   args.ForumID,
		PostType:  postType,
		Title:     args.Title,
		Body:      args.Body,
		Tags:      args.Tags,
		Images:    args.Images,
		ImageURLs: args.ImageURLs,
		SourceURL: args.SourceURL,
		VideoURLs: args.VideoURLs,
	})
	if err != nil {
		return chineseInLAErrorResult(err)
	}

	toolResult := chineseInLAJSONResult(result)
	if result.PreviewImage == "" {
		return toolResult
	}
	preview, err := os.ReadFile(result.PreviewImage)
	if err != nil {
		return chineseInLAErrorResult(fmt.Errorf("read the required headless preview %q: %w; do not publish until the prepared form can be reviewed", result.PreviewImage, err))
	}
	toolResult.Content = append(toolResult.Content, MCPContent{
		Type:     "image",
		MimeType: "image/png",
		Data:     base64.StdEncoding.EncodeToString(preview),
	})
	return toolResult
}

func (s *AppServer) handleChineseInLAPublishPost(ctx context.Context, args ChineseInLAPublishPostArgs) *MCPToolResult {
	if !args.ConfirmPublish {
		return chineseInLARefusal("Refusing to publish without the user's second explicit confirmation after reviewing the prepared form or screenshot (confirm_publish=true).")
	}
	if strings.TrimSpace(args.DraftID) == "" {
		return chineseInLARefusal("Refusing to publish without the draft_id returned by chineseinla_prepare_post.")
	}
	if unavailable := s.chineseInLAUnavailable(); unavailable != nil {
		return unavailable
	}

	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()
	result, err := s.chineseInLAService.PublishPrepared(ctx, strings.TrimSpace(args.DraftID), true)
	if err != nil {
		return chineseInLAErrorResult(err)
	}
	return chineseInLAJSONResult(result)
}

func (s *AppServer) chineseInLAUnavailable() *MCPToolResult {
	if s.chineseInLAService != nil {
		return nil
	}
	return chineseInLARefusal("ChineseInLA support is not configured in this server process. Start the main MCP server, which configures both services, or use NewAppServerWithChineseInLA when embedding it.")
}

func chineseInLAJSONResult(value any) *MCPToolResult {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return chineseInLAErrorResult(fmt.Errorf("encode ChineseInLA result: %w", err))
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(data)}}}
}

func chineseInLALoginSessionResult(status chineseinla.LoginSessionStatus) *MCPToolResult {
	result := chineseInLAJSONResult(status)
	if status.Screenshot != nil && !status.LoggedIn && !result.IsError {
		result.Content = append(result.Content, MCPContent{
			Type:     "image",
			MimeType: "image/png",
			Data:     base64.StdEncoding.EncodeToString(status.Screenshot),
		})
	}
	return result
}

func chineseInLAErrorResult(err error) *MCPToolResult {
	action := ""
	switch {
	case errors.Is(err, chineseinla.ErrNotLoggedIn):
		action = " Open ChineseInLA login, complete sign-in in the isolated browser profile, then retry."
	case errors.Is(err, chineseinla.ErrCaptcha):
		action = " Complete the CAPTCHA or human verification manually; this integration will not bypass it."
	case errors.Is(err, chineseinla.ErrLoginSessionNotFound), errors.Is(err, chineseinla.ErrLoginSessionExpired):
		action = " Start a new ChineseInLA login session and use its exact session_id."
	case errors.Is(err, chineseinla.ErrLoginAttemptsExceeded):
		action = " Start a new ChineseInLA login session before trying again."
	}
	return chineseInLARefusal("ChineseInLA operation failed: " + err.Error() + action)
}

func chineseInLARefusal(message string) *MCPToolResult {
	return &MCPToolResult{
		IsError: true,
		Content: []MCPContent{{Type: "text", Text: message}},
	}
}
