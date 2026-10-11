package xiaohongshu

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

import (
	"context"
	"os"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/stretchr/testify/require"
)

// staleButtonPage 一楼 4 条回复只露 1 条。鼠标第一次移到「展开」按钮上，页面就把按钮重画成一个新的
// （刚找到的那个失效，点不上）；点新的按钮，回复写进页面状态，和真实页面一样。
const staleButtonPage = `data:text/html;charset=utf-8,
<div class="parent-comment">
  <div id="comment-root" class="comment-item">楼主</div>
  <div id="comment-r1" class="comment-item">回复 1</div>
  <div class="show-more" style="width:280px;height:32px">展开 3 条回复</div>
</div>
<script>
window.__INITIAL_STATE__ = {note: {noteDetailMap: {feed: {comments: {list: [
  {id: "root", subCommentCount: "4", subComments: [{id: "r1"}]}
]}}}}};
let redrawn = false;
document.addEventListener("mouseover", e => {
  const button = e.target.closest(".show-more");
  if (!button || redrawn) return;
  redrawn = true;
  button.replaceWith(button.cloneNode(true));
});
document.addEventListener("click", e => {
  const button = e.target.closest(".show-more");
  if (!button) return;
  window.__INITIAL_STATE__.note.noteDetailMap.feed.comments.list[0].subComments.push({id: "r2"}, {id: "r3"}, {id: "r4"});
  button.remove();
});
</script>`

// TestAFailedExpandClickIsRetriedWithAFreshButton 复现 2026-10-11：同一楼第一次读只读回 1/4 条（按钮刚找到就
// 失效，一下没点开就放弃了），再读一次才读全。一轮没点开不算数，要重新找按钮再点。要设 XHS_BROWSER_BIN 才跑。
func TestAFailedExpandClickIsRetriedWithAFreshButton(t *testing.T) {
	bin := os.Getenv("XHS_BROWSER_BIN")
	if bin == "" {
		t.Skip("设 XHS_BROWSER_BIN 才跑真浏览器测试")
	}
	url := launcher.New().Bin(bin).Headless(true).MustLaunch()
	browser := rod.New().ControlURL(url).MustConnect()
	defer browser.MustClose()
	page := browser.MustPage(staleButtonPage)
	page.MustWaitLoad()

	require.Positive(t, expandThread(context.Background(), page, "feed", "root"))
	thread, err := readThreadState(page, "feed", "root")
	require.NoError(t, err)
	require.True(t, thread.Complete)
	require.Len(t, thread.Thread.SubComments, 4)
}
