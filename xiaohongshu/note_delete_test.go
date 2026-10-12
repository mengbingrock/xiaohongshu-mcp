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
