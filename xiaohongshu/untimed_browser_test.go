package xiaohongshu

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// TestFoundElementsOutliveTheLookupDeadline 用真浏览器复现 2026-10-10 两次私信没发出的原因：带期限找到的元素，
// 期限过后再点击、输入。要设 XHS_BROWSER_BIN（Chromium 可执行文件）才跑。
func TestFoundElementsOutliveTheLookupDeadline(t *testing.T) {
	bin := os.Getenv("XHS_BROWSER_BIN")
	if bin == "" {
		t.Skip("设 XHS_BROWSER_BIN 才跑真浏览器测试")
	}
	url := launcher.New().Bin(bin).Headless(true).MustLaunch()
	browser := rod.New().ControlURL(url).MustConnect()
	defer browser.MustClose()
	page := browser.MustPage("data:text/html,<div id=box contenteditable=true style='width:200px;height:40px'></div>")
	page.MustWaitLoad()

	box, err := findUntimedWithin(page, 300*time.Millisecond, func(p *rod.Page) (*rod.Element, error) { return p.Element("#box") })
	require.NoError(t, err)
	time.Sleep(600 * time.Millisecond) // 找元素的期限已经过了
	require.NoError(t, humanize.Click(box))
	require.NoError(t, humanize.Type(context.Background(), box, "你好"))
	require.NoError(t, box.Focus())
	require.Equal(t, "你好", box.MustText())
}
