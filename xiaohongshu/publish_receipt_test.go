package xiaohongshu

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

import (
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishResponseIsConfirmedOnlyWithANoteID(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		state string
		id    string
	}{
		{"成功带 id", `{"success":true,"code":0,"data":{"id":"6a1"}}`, StateConfirmed, "6a1"},
		{"成功带 note_id", `{"success":true,"code":0,"data":{"note_id":"6a2"}}`, StateConfirmed, "6a2"},
		{"嵌套在 note 里", `{"success":true,"code":0,"data":{"note":{"noteId":"6a3"}}}`, StateConfirmed, "6a3"},
		{"成功没有 id：交给最近一篇笔记核对", `{"success":true,"code":0,"data":{}}`, StateUnknown, ""},
		{"平台拒绝", `{"success":false,"code":-9131,"msg":"内容违规"}`, StateRejected, ""},
		{"读不懂", `<html>`, StateUnknown, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := parsePublishResponse(200, []byte(tc.body))
			assert.Equal(t, tc.state, r.State)
			assert.Equal(t, tc.id, r.NoteID)
		})
	}
}

func TestThePublishRequestIsRecognizedByPathAndTitle(t *testing.T) {
	u, _ := url.Parse("https://edith.xiaohongshu.com/web_api/sns/v2/note")
	assert.True(t, publishPath.MatchString(u.Path))
	assert.False(t, publishPath.MatchString("/web_api/sns/v5/creator/topic/template/list"))
	body := `{"common":{"title":"合伙闹翻了，先保存这些材料","desc":"正文"},"image_info":{}}`
	assert.True(t, jsonHasText(body, "合伙闹翻了，先保存这些材料"))
	assert.True(t, jsonHasText(body, "合伙闹翻了， 先保存这些材料")) // 忽略空白，同评论
	assert.False(t, jsonHasText(body, "别的标题"))
	assert.False(t, jsonHasText("not json", "正文"))
}

func TestADropdownMustShowTheChosenOptionOrNothingIsPublished(t *testing.T) {
	assert.NoError(t, requireShown("可见范围", "仅自己可见", "仅自己可见"))
	assert.NoError(t, requireShown("内容类型声明", "笔记含AI合成内容 ", "笔记含AI合成内容"))
	assert.ErrorContains(t, requireShown("可见范围", "公开可见", "仅自己可见"), "可见范围没有选上「仅自己可见」")
	assert.ErrorContains(t, requireShown("内容类型声明", "添加内容类型声明", "笔记含AI合成内容"), "没有发布")
	assert.Error(t, requireShown("可见范围", "", "仅自己可见")) // 读不到也算没选上
}

// settingsPage 发布页里可见范围和内容类型声明两个下拉框选完后的样子（class 照 2026-10-11 探查到的结构）。
const settingsPage = `data:text/html;charset=utf-8,
<div class="permission-card-wrapper"><div class="d-select-wrapper"><div class="d-select-content">仅自己可见</div></div></div>
<div class="d-select-wrapper"><div class="d-select-content">选择地点</div></div>
<div class="wrapper"><div class="d-select-wrapper"><div class="d-select-content">笔记含AI合成内容</div></div></div>`

// TestTheReadBackLooksAtTheRightDropdowns 回读读的是可见范围和内容类型声明那两个下拉框，不是页面上别的下拉框。
// 要设 XHS_BROWSER_BIN 才跑。点选本身在真实页面上用发布页探查的「只选不发」验证。
func TestTheReadBackLooksAtTheRightDropdowns(t *testing.T) {
	bin := os.Getenv("XHS_BROWSER_BIN")
	if bin == "" {
		t.Skip("设 XHS_BROWSER_BIN 才跑真浏览器测试")
	}
	browser := rod.New().ControlURL(launcher.New().Bin(bin).Headless(true).MustLaunch()).MustConnect()
	defer browser.MustClose()
	page := browser.MustPage(settingsPage).Timeout(30 * time.Second)
	page.MustWaitLoad()

	shown, err := elementText(page, visibilitySelector)
	require.NoError(t, err)
	assert.Equal(t, "仅自己可见", shown)
	assert.Equal(t, "笔记含AI合成内容", shownContentType(page)) // 选完占位文字就没了，按选中的项认出这个下拉框
}
