package xiaohongshu

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 结构同网页的 /api/im/web/v3/chats 和 messages/history（2026-10-10 实测），内容是合成的。
const chatsFixture = `{"code":0,"success":true,"data":{"chats":[
	{"chat_user_id":"u1","info":{"nickname":"YC"}},
	{"chat_user_id":"u2","info":{"nickname":"同名"}},
	{"chat_user_id":"u3","info":{"nickname":"同名"}},
	{"chat_user_id":"u4","info":{"nickname":""}}]}}`

func TestConversationNicknameRefusesMissingAndAmbiguousChats(t *testing.T) {
	nickname, err := conversationNickname(json.RawMessage(chatsFixture), "u1")
	require.NoError(t, err)
	assert.Equal(t, "YC", nickname)

	_, err = conversationNickname(json.RawMessage(chatsFixture), "stranger")
	assert.True(t, errors.Is(err, ErrConversationNotFound), "没有会话就不读不回（不新建会话）")
	_, err = conversationNickname(json.RawMessage(chatsFixture), "u2")
	assert.ErrorContains(t, err, "不按昵称点开", "重名不猜")
	_, err = conversationNickname(json.RawMessage(chatsFixture), "u4")
	assert.ErrorContains(t, err, "没有昵称")
}

func TestHistoryFitsOnlyMessagesWithThatUser(t *testing.T) {
	ours := `{"data":{"out_message_list":[{"sender_id":"u1","receiver_id":"me"},{"sender_id":"me","receiver_id":"u1"}]}}`
	assert.True(t, historyFits(json.RawMessage(ours), "u1"))
	assert.False(t, historyFits(json.RawMessage(ours), "u2"))
	assert.True(t, historyFits(json.RawMessage(`{"data":{"out_message_list":[]}}`), "u1"), "没有消息时交给窗口标题核对")
	assert.False(t, historyFits(json.RawMessage(`not json`), "u1"))
}

func TestDMRequestIsRecognizedByUserAndContentEvenWhenNested(t *testing.T) {
	nested := `{"receiver_id":"u1","content":"{\"content\":\"第一行第二行\",\"content_type\":1}"}`
	assert.True(t, dmRequestMatches(nested, "u1", "第一行\n第二行"), "嵌套的 JSON 字符串里也认得，空白不算")
	assert.False(t, dmRequestMatches(nested, "u2", "第一行\n第二行"), "对方不对")
	assert.False(t, dmRequestMatches(nested, "u1", "别的话"), "正文不对")
	assert.False(t, dmRequestMatches(`{"chat_user_id":"u1"}`, "u1", "第一行"), "标已读这类请求没有正文")
	assert.False(t, dmRequestMatches(`form=1&u1=x`, "u1", "x"), "不是 JSON 不认")
}
