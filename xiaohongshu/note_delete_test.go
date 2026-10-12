package xiaohongshu

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNoteInBodiesFindsTheListThatHasTheNoteAndItsTitle(t *testing.T) {
	bodies := map[string]json.RawMessage{
		"/api/galaxy/user/info": json.RawMessage(`{"data":{"id":"u1","name":"YC"}}`),
		"/api/galaxy/v2/creator/note/user/posted": json.RawMessage(
			`{"data":{"notes":[{"id":"n1","display_title":"第一篇"},{"note_id":"n2","title":"第二篇"}]}}`),
	}
	path, title := noteInBodies(bodies, "n2")
	assert.Equal(t, "/api/galaxy/v2/creator/note/user/posted", path)
	assert.Equal(t, "第二篇", title)

	path, _ = noteInBodies(bodies, "n3")
	assert.Empty(t, path, "列表里没有就是没有，不拿别的接口凑")

	path, title = noteInBodies(map[string]json.RawMessage{"/x": json.RawMessage(`[{"noteId":"n9"}]`)}, "n9")
	assert.Equal(t, "/x", path)
	assert.Empty(t, title)
}

func TestDialogTitleMatchesTruncatedTitles(t *testing.T) {
	dialog := "删除笔记\n删除后将无法恢复，确定要删除《嘉义搭子12.27-》这篇笔记吗\n取消\n确定"
	assert.True(t, dialogTitleMatches(dialog, "嘉义搭子12.27-12.28"))
	assert.False(t, dialogTitleMatches(dialog, "温哥华来chat 回吧"))
	assert.True(t, dialogTitleMatches("确定要删除《很长的标题…》吗", "很长的标题后面还有字"))
	assert.False(t, dialogTitleMatches("确定要删除《》吗", "随便"), "书名号里是空的认不出，不删")
	assert.True(t, dialogTitleMatches("确定删除这篇笔记吗", "随便"), "弹窗没写标题：卡片已经按 id 认过")
}
