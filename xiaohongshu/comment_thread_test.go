package xiaohongshu

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 页面状态里一楼的样子（字段名同网页的 __INITIAL_STATE__，内容是合成的）。
const threadState = `{
	"id": "root", "noteId": "note", "content": "楼主评论", "subCommentCount": "3",
	"userInfo": {"userId": "us", "nickname": "我们"},
	"subComments": [
		{"id": "r1", "content": "第一条回复", "userInfo": {"userId": "author"}},
		{"id": "r2", "content": "我们的跟进", "userInfo": {"userId": "us"},
		 "targetComment": {"id": "r1", "userInfo": {"userId": "author"}}},
		{"id": "r3", "content": "回我们的跟进", "userInfo": {"userId": "author"},
		 "targetComment": {"id": "r2", "userInfo": {"userId": "us"}}}
	]
}`

func TestParseThreadKeepsRepliesAndWhatTheyReplyTo(t *testing.T) {
	thread, err := parseThread("note", threadState)
	require.NoError(t, err)

	assert.Equal(t, "note", thread.FeedID)
	assert.Equal(t, "root", thread.Thread.ID)
	require.Len(t, thread.Thread.SubComments, 3)
	assert.Nil(t, thread.Thread.SubComments[0].TargetComment, "直接回楼主时页面不给被回复的那条")
	assert.Equal(t, "r2", thread.Thread.SubComments[2].TargetComment.ID)
	assert.Equal(t, "us", thread.Thread.SubComments[2].TargetComment.UserInfo.UserID)
	assert.True(t, thread.Complete)
}

func TestThreadCompleteComparesWithTheShownReplyCount(t *testing.T) {
	assert.False(t, threadComplete(Comment{SubCommentCount: "5", SubComments: make([]Comment, 3)}))
	assert.True(t, threadComplete(Comment{SubCommentCount: "3", SubComments: make([]Comment, 3)}))
	assert.True(t, threadComplete(Comment{SubCommentCount: ""}), "读不出回复数时按读全了算")
}

func TestParseThreadWithoutTheThreadIsNotFound(t *testing.T) {
	_, err := parseThread("note", "")
	assert.True(t, errors.Is(err, ErrCommentNotFound))
}
