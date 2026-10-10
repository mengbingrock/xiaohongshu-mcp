package xiaohongshu

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeleteRequestMustBindBothNoteAndComment(t *testing.T) {
	assert.True(t, deleteRequestMatches(`{"note_id":"n","comment_id":"c"}`, "n", "c"))
	assert.True(t, deleteRequestMatches(`{"noteId":"n","commentId":"c"}`, "n", "c"))
	assert.False(t, deleteRequestMatches(`{"note_id":"n","comment_id":"other"}`, "n", "c"))
	assert.False(t, deleteRequestMatches(`{"note_id":"n"}`, "n", "c"), "只有笔记 id 不算")
	assert.False(t, deleteRequestMatches(`{"noteId":"n","commentId":"c","comment_id":"other"}`, "n", "c"), "两种写法矛盾不认")
	assert.False(t, deleteRequestMatches(`not json`, "n", "c"))
}

func TestOnlyTheLoggedInAccountsOwnCommentIsDeleted(t *testing.T) {
	assert.NoError(t, judgeOwnership(commentProof{Me: "me", Owner: "me", Found: true}))
	assert.True(t, errors.Is(judgeOwnership(commentProof{Me: "me", Owner: "someone", Found: true}), ErrNotOwnComment))
	assert.True(t, errors.Is(judgeOwnership(commentProof{Me: "me", Found: false}), ErrCommentNotFound))
	var access *PageAccessError
	assert.True(t, errors.As(judgeOwnership(commentProof{Owner: "me", Found: true}), &access), "没登录：按登录失效报")
	assert.Equal(t, CodeLoginRequired, access.Code)
}
