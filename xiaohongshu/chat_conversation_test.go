package xiaohongshu

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 结构同网页的 /api/im/web/v3/chats 和 messages/history（2026-10-10 实测），内容是合成的。
const chatsFixture = `{"code":0,"success":true,"data":{"chats":[
	{"chat_user_id":"u1","user_id":"me","info":{"nickname":"YC"}},
	{"chat_user_id":"u2","info":{"nickname":"同名"}},
	{"chat_user_id":"u3","info":{"nickname":"同名"}},
	{"chat_user_id":"u4","info":{"nickname":""}}]}}`

func TestFindChatRefusesMissingAndAmbiguousChats(t *testing.T) {
	chat, err := findChat(json.RawMessage(chatsFixture), "u1")
	require.NoError(t, err)
	assert.Equal(t, "YC", chat.Info.Nickname)
	assert.Equal(t, "me", chat.UserID, "会话列表里的 user_id 是我们自己")

	_, err = findChat(json.RawMessage(chatsFixture), "stranger")
	assert.True(t, errors.Is(err, ErrConversationNotFound), "没有会话就不读不回（不新建会话）")
	_, err = findChat(json.RawMessage(chatsFixture), "u2")
	assert.ErrorContains(t, err, "不按昵称点开", "重名不猜")
	_, err = findChat(json.RawMessage(chatsFixture), "u4")
	assert.ErrorContains(t, err, "没有昵称")
}

func TestOwnMessageConfirmsTheReplyFromHistory(t *testing.T) {
	sent := time.UnixMilli(1791566538770)
	history := `{"data":{"out_message_list":[
		{"id":"m3","sender_id":"me","created_at":1791566540000,"content":"{\"content\":\"第一行第二行\",\"content_type\":1}"},
		{"id":"m2","sender_id":"u1","created_at":1791566539000,"content":"{\"content\":\"第一行\\n第二行\",\"content_type\":1}"},
		{"id":"m1","sender_id":"me","created_at":1791566000000,"content":"{\"content\":\"第一行\\n第二行\",\"content_type\":1}"}]}}`
	id, ok := ownMessage(json.RawMessage(history), "me", "第一行\n第二行", sent)
	assert.True(t, ok)
	assert.Equal(t, "m3", id, "我们在发送之后发的那条（换行被吞也认得）；对方说的、更早的同样文字都不算")
	_, ok = ownMessage(json.RawMessage(history), "me", "别的话", sent)
	assert.False(t, ok)
	_, ok = ownMessage(json.RawMessage(history), "", "第一行第二行", sent)
	assert.False(t, ok, "不知道自己是谁就不确认")
}

func TestOnlyAnUnobservedSubmissionIsRechecked(t *testing.T) {
	unseen := &InteractionError{Stage: "confirm", State: StateUnknown, Cause: errors.New("没有拿到可靠的业务回执")}
	assert.True(t, unconfirmed(unseen))
	assert.False(t, unconfirmed(&InteractionError{Stage: "confirm", State: StateRejected, Cause: errors.New("拒绝")}))
	assert.False(t, unconfirmed(&InteractionError{Stage: "click", State: StateUnknown, Cause: errors.New("点击失败")}))
	blocked := &InteractionError{Stage: "confirm", State: StateUnknown, Cause: &PageAccessError{Code: CodeSecurityVerification}}
	assert.False(t, unconfirmed(blocked), "被拦了不再去碰页面")
	assert.False(t, unconfirmed(nil))
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
