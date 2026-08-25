package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/internal/chineseinla"
)

type fakeChineseInLAService struct {
	loginResult      chineseinla.LoginStatus
	checkLoginResult chineseinla.LoginStatus
	forumsResult     []chineseinla.Forum
	prepareResult    chineseinla.PrepareResult
	publishResult    chineseinla.PublishResult
	err              error
	prepareCalls     int
	publishCalls     int
	preparedRequest  chineseinla.PrepareRequest
	publishedDraftID string
	publishConfirmed bool
}

func (f *fakeChineseInLAService) Login(context.Context) (chineseinla.LoginStatus, error) {
	return f.loginResult, f.err
}

func (f *fakeChineseInLAService) CheckLogin(context.Context) (chineseinla.LoginStatus, error) {
	return f.checkLoginResult, f.err
}

func (f *fakeChineseInLAService) Forums(context.Context) ([]chineseinla.Forum, error) {
	return f.forumsResult, f.err
}

func (f *fakeChineseInLAService) Prepare(_ context.Context, request chineseinla.PrepareRequest) (chineseinla.PrepareResult, error) {
	f.prepareCalls++
	f.preparedRequest = request
	return f.prepareResult, f.err
}

func (f *fakeChineseInLAService) PublishPrepared(_ context.Context, draftID string, confirmed bool) (chineseinla.PublishResult, error) {
	f.publishCalls++
	f.publishedDraftID = draftID
	f.publishConfirmed = confirmed
	return f.publishResult, f.err
}

func TestChineseInLAPrepareRequiresFirstConfirmation(t *testing.T) {
	t.Parallel()
	fake := &fakeChineseInLAService{}
	server := &AppServer{chineseInLAService: fake}

	result := server.handleChineseInLAPreparePost(t.Context(), ChineseInLAPreparePostArgs{
		ForumID:  44,
		PostType: "other",
		Title:    "不会被填写",
		Body:     "不会被填写",
	})

	assert.True(t, result.IsError)
	assert.Zero(t, fake.prepareCalls)
	assert.Contains(t, result.Content[0].Text, "confirm_preparation=true")
}

func TestChineseInLAPrepareReturnsHeadlessPreviewImage(t *testing.T) {
	t.Parallel()
	previewPath := filepath.Join(t.TempDir(), "preview.png")
	preview := []byte("test-png-payload")
	require.NoError(t, os.WriteFile(previewPath, preview, 0o600))

	fake := &fakeChineseInLAService{prepareResult: chineseinla.PrepareResult{
		Status:       "ready_to_preview",
		DraftID:      "draft-123",
		PreviewImage: previewPath,
	}}
	server := &AppServer{chineseInLAService: fake}

	result := server.handleChineseInLAPreparePost(t.Context(), ChineseInLAPreparePostArgs{
		ForumID:            44,
		PostType:           "other",
		Title:              "测试预览帖子",
		Body:               "先看截图，再决定是否发布。",
		ConfirmPreparation: true,
	})

	require.False(t, result.IsError)
	require.Len(t, result.Content, 2)
	assert.Equal(t, "text", result.Content[0].Type)
	assert.Contains(t, result.Content[0].Text, `"draft_id": "draft-123"`)
	assert.Equal(t, "image", result.Content[1].Type)
	decoded, err := base64.StdEncoding.DecodeString(result.Content[1].Data)
	require.NoError(t, err)
	assert.Equal(t, preview, decoded)
	assert.Equal(t, chineseinla.PostTypeOther, fake.preparedRequest.PostType)
}

func TestChineseInLAPublishRequiresSecondConfirmationAndExactDraft(t *testing.T) {
	t.Parallel()
	fake := &fakeChineseInLAService{publishResult: chineseinla.PublishResult{
		Status:   "published",
		TopicURL: chineseinla.BaseURL + "/f/page_viewtopic/t_123.html",
	}}
	server := &AppServer{chineseInLAService: fake}

	refused := server.handleChineseInLAPublishPost(t.Context(), ChineseInLAPublishPostArgs{DraftID: "draft-123"})
	assert.True(t, refused.IsError)
	assert.Zero(t, fake.publishCalls)

	result := server.handleChineseInLAPublishPost(t.Context(), ChineseInLAPublishPostArgs{
		DraftID:        " draft-123 ",
		ConfirmPublish: true,
	})
	require.False(t, result.IsError)
	assert.Equal(t, 1, fake.publishCalls)
	assert.Equal(t, "draft-123", fake.publishedDraftID)
	assert.True(t, fake.publishConfirmed)
}

func TestChineseInLAErrorsExplainManualVerification(t *testing.T) {
	t.Parallel()
	fake := &fakeChineseInLAService{err: chineseinla.ErrCaptcha}
	server := &AppServer{chineseInLAService: fake}

	result := server.handleChineseInLACheckLogin(t.Context())

	assert.True(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "will not bypass")
	assert.True(t, errors.Is(fake.err, chineseinla.ErrCaptcha))
}

func TestChineseInLAPublishConfirmationGateThroughMCP(t *testing.T) {
	fake := &fakeChineseInLAService{}
	router := setupRoutes(NewAppServerWithChineseInLA(NewXiaohongshuService(), fake, ""))
	server := httptest.NewServer(router)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"tools/call",
		"params":{
			"name":"chineseinla_publish_post",
			"arguments":{"draft_id":"draft-123","confirm_publish":false}
		}
	}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	var rpcResult struct {
		Error  any `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&rpcResult))
	require.Nil(t, rpcResult.Error)
	assert.True(t, rpcResult.Result.IsError)
	require.NotEmpty(t, rpcResult.Result.Content)
	assert.Contains(t, rpcResult.Result.Content[0].Text, "confirm_publish=true")
	assert.Zero(t, fake.publishCalls)
}
