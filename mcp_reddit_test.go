package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/internal/reddit"
)

type fakeRedditService struct {
	loginResult     reddit.LoginStatus
	forumsResult    reddit.ListForumsResult
	restrictResult  reddit.RestrictionsResult
	postsResult     reddit.ListPostsResult
	readResult      reddit.ReadPostResult
	publishResult   reddit.PublishResult
	commentResult   reddit.CommentResult
	err             error
	publishCalls    int
	commentCalls    int
	publishRequest  reddit.PublishRequest
	commentRequest  reddit.CommentRequest
	forumsRequest   reddit.ListForumsRequest
	restrictRequest reddit.RestrictionsRequest
	postsRequest    reddit.ListPostsRequest
	readRequest     reddit.ReadPostRequest
}

func (f *fakeRedditService) CheckLogin(context.Context, string) (reddit.LoginStatus, error) {
	return f.loginResult, f.err
}

func (f *fakeRedditService) ListForums(_ context.Context, request reddit.ListForumsRequest) (reddit.ListForumsResult, error) {
	f.forumsRequest = request
	return f.forumsResult, f.err
}

func (f *fakeRedditService) Restrictions(_ context.Context, request reddit.RestrictionsRequest) (reddit.RestrictionsResult, error) {
	f.restrictRequest = request
	return f.restrictResult, f.err
}

func (f *fakeRedditService) ListPosts(_ context.Context, request reddit.ListPostsRequest) (reddit.ListPostsResult, error) {
	f.postsRequest = request
	return f.postsResult, f.err
}

func (f *fakeRedditService) ReadPost(_ context.Context, request reddit.ReadPostRequest) (reddit.ReadPostResult, error) {
	f.readRequest = request
	return f.readResult, f.err
}

func (f *fakeRedditService) Publish(_ context.Context, request reddit.PublishRequest) (reddit.PublishResult, error) {
	f.publishCalls++
	f.publishRequest = request
	return f.publishResult, f.err
}

func (f *fakeRedditService) Comment(_ context.Context, request reddit.CommentRequest) (reddit.CommentResult, error) {
	f.commentCalls++
	f.commentRequest = request
	return f.commentResult, f.err
}

func TestRedditReadToolsPassExactIdentifiers(t *testing.T) {
	t.Parallel()
	fake := &fakeRedditService{
		forumsResult:   reddit.ListForumsResult{Status: "ok", Forums: []reddit.Forum{{Name: "COROLLA"}}},
		restrictResult: reddit.RestrictionsResult{Status: "ok", Subreddit: "/r/COROLLA", Allow: []string{"self", "link"}},
		postsResult:    reddit.ListPostsResult{Status: "ok", Posts: []reddit.Post{{ID: "t3_1uyxj8j"}}},
		readResult:     reddit.ReadPostResult{Status: "ok", Post: reddit.Post{ID: "t3_1uyxj8j"}},
	}
	server := &AppServer{redditService: fake}

	listedForums := server.handleRedditListForums(t.Context(), RedditListForumsArgs{ProfileKey: "abc", Query: "corolla", Limit: 5})
	require.False(t, listedForums.IsError)
	assert.Contains(t, listedForums.Content[0].Text, `"name": "COROLLA"`)
	assert.Equal(t, reddit.ListForumsRequest{ProfileKey: "abc", Query: "corolla", Limit: 5}, fake.forumsRequest)

	restrictions := server.handleRedditRestrictions(t.Context(), RedditRestrictionsArgs{ProfileKey: "abc", Subreddit: "COROLLA"})
	require.False(t, restrictions.IsError)
	assert.Contains(t, restrictions.Content[0].Text, `"allow": [`)
	assert.Equal(t, reddit.RestrictionsRequest{ProfileKey: "abc", Subreddit: "COROLLA"}, fake.restrictRequest)

	listedPosts := server.handleRedditListPosts(t.Context(), RedditListPostsArgs{ProfileKey: "abc", Subreddit: "COROLLA", Sort: "new", Limit: 10})
	require.False(t, listedPosts.IsError)
	assert.Contains(t, listedPosts.Content[0].Text, `"id": "t3_1uyxj8j"`)
	assert.Equal(t, reddit.ListPostsRequest{ProfileKey: "abc", Subreddit: "COROLLA", Sort: "new", Limit: 10}, fake.postsRequest)

	read := server.handleRedditReadPost(t.Context(), RedditReadPostArgs{ProfileKey: "abc", Subreddit: "COROLLA", PostID: "t3_1uyxj8j", CommentLimit: 25})
	require.False(t, read.IsError)
	assert.Equal(t, reddit.ReadPostRequest{ProfileKey: "abc", Subreddit: "COROLLA", PostID: "t3_1uyxj8j", CommentLimit: 25}, fake.readRequest)
}

func TestRedditPublishAndCommentRequireConfirmation(t *testing.T) {
	t.Parallel()
	fake := &fakeRedditService{
		publishResult: reddit.PublishResult{Status: "published", PostID: "t3_newpost"},
		commentResult: reddit.CommentResult{Status: "published", CommentID: "t1_newcomment"},
	}
	server := &AppServer{redditService: fake}

	refusedPost := server.handleRedditPublish(t.Context(), RedditPublishArgs{ProfileKey: "abc", Subreddit: "COROLLA", Title: "Test"})
	assert.True(t, refusedPost.IsError)
	assert.Zero(t, fake.publishCalls)

	published := server.handleRedditPublish(t.Context(), RedditPublishArgs{
		ProfileKey: "abc", Subreddit: "COROLLA", PostType: "self", Title: "Test", Body: "Body", ConfirmPublish: true,
	})
	require.False(t, published.IsError)
	assert.Equal(t, 1, fake.publishCalls)
	assert.True(t, fake.publishRequest.ConfirmPublish)

	refusedComment := server.handleRedditComment(t.Context(), RedditCommentArgs{ProfileKey: "abc", Subreddit: "COROLLA", PostID: "t3_post", Body: "Hello"})
	assert.True(t, refusedComment.IsError)
	assert.Zero(t, fake.commentCalls)

	commented := server.handleRedditComment(t.Context(), RedditCommentArgs{
		ProfileKey: "abc", Subreddit: "COROLLA", PostID: "t3_post", Body: "Hello", ConfirmComment: true,
	})
	require.False(t, commented.IsError)
	assert.Equal(t, 1, fake.commentCalls)
	assert.True(t, fake.commentRequest.ConfirmComment)
}
