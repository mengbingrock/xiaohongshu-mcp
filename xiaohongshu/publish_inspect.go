// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

package xiaohongshu

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// inspectKeywords 发布页上要找的控件文字：AI 合成声明、内容类型声明、可见范围、原创声明。
var inspectKeywords = []string{"AI", "声明", "可见", "原创"}

// PublishInspection 发布页探查的结果：补 AI 合成声明和笔记 id 回执之前（或平台改版后），看清发布表单和接口。
type PublishInspection struct {
	Form               []string                   `json:"form"`                  // 上传一张图之后的页面结构（每行一个元素，缩进 = 层级）
	Matches            []string                   `json:"matches"`               // 文字含 inspectKeywords 的最小元素：路径、文字、外层 HTML（截断）
	DeclarationClicked string                     `json:"declaration_clicked"`   // 点开的「声明」类控件的文字（不是原创声明）
	Declaration        []string                   `json:"declaration,omitempty"` // 点开它之后的页面结构（只看不选）
	Visibility         []string                   `json:"visibility,omitempty"`  // 点开可见范围下拉后的选项文字（只看不选）
	Requests           []string                   `json:"requests"`              // 打开、上传、点控件时网页发的创作中心请求：方法 + 域名路径
	Responses          map[string]json.RawMessage `json:"responses"`             // 这些请求的 JSON 响应原文，按路径，同一路径留最后一次
	Applied            map[string]string          `json:"applied,omitempty"`     // 「只选不发」：选了什么、回读的结果（或错误）
	LeftWithDialog     bool                       `json:"left_with_dialog"`      // 离开时弹了「确定离开」之类的对话框（确认离开，不存草稿）
}

// PublishSettings 探查时「只选不发」要试的设置：用发布时同一套函数选上、回读，然后照样不发布。空的不试。
type PublishSettings struct {
	Visibility  string `json:"visibility,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// InspectPublish 打开创作中心的发布页，上传一张图，记下表单结构、声明和可见范围控件、接口；点开声明和可见范围
// 看选项但不选；给了 settings 就用发布时同一套函数选上并回读（只选不发）；不填标题正文、不点发布；
// 最后离开页面（有「是否离开」对话框就确认离开）。图片会上传到平台（编辑器选图即上传），但不会发出笔记。
func InspectPublish(ctx context.Context, page *rod.Page, imagePath string, settings PublishSettings) (*PublishInspection, error) {
	capture, err := captureAPI(ctx, page, isCreatorAPI)
	if err != nil {
		return nil, err
	}
	defer capture.close()
	action, err := NewPublishImageAction(page)
	if err != nil {
		return nil, err
	}
	p := action.page.Context(ctx).Timeout(3 * time.Minute)
	if err := uploadImages(p, action.trace, []string{imagePath}); err != nil {
		return nil, errors.Wrap(err, "上传探查用的图片失败")
	}
	humanize.Delay(ctx, humanize.Reading)
	out := &PublishInspection{Form: skeleton(p), Matches: keywordElements(p)}

	if el, err := p.Timeout(3 * time.Second).ElementByJS(rod.Eval(declarationJS)); err == nil && el != nil {
		out.DeclarationClicked, _ = el.Text()
		if err := humanize.Click(el); err != nil {
			logrus.Warnf("点开声明控件失败: %v", err)
		} else {
			time.Sleep(1500 * time.Millisecond) // 技术等待：等下拉或弹窗渲染
			out.Declaration = skeleton(p)
			closeOverlay(p)
		}
	}
	if dropdown, err := p.Timeout(3 * time.Second).Element("div.permission-card-wrapper div.d-select-content"); err == nil {
		if err := humanize.Click(dropdown); err == nil {
			time.Sleep(800 * time.Millisecond)
			out.Visibility = optionTexts(p)
			closeOverlay(p)
		}
	}
	if settings != (PublishSettings{}) {
		out.Applied = applySettings(p, settings)
	}
	out.Requests, out.Responses = capture.requestLog(), capture.changedSince(map[string]int{})
	out.LeftWithDialog = leaveWithoutSaving(p)
	return out, nil
}

// applySettings 用发布时同一套函数选可见范围和内容类型声明，记下回读结果（或错误）。不点发布。
func applySettings(p *rod.Page, settings PublishSettings) map[string]string {
	applied := map[string]string{}
	if settings.Visibility != "" {
		if err := selectVisibility(p, settings.Visibility); err != nil {
			applied["visibility"] = "失败: " + err.Error()
		} else {
			applied["visibility"], _ = elementText(p, visibilitySelector)
		}
	}
	if settings.ContentType != "" {
		if err := setContentType(p, settings.ContentType); err != nil {
			applied["content_type"] = "失败: " + err.Error()
		} else {
			applied["content_type"] = shownContentType(p)
		}
	}
	return applied
}

// isCreatorAPI 创作中心和上传用到的小红书接口（不含静态资源）。
func isCreatorAPI(u *url.URL) bool {
	if !strings.HasSuffix(u.Hostname(), "xiaohongshu.com") {
		return false
	}
	return strings.Contains(u.Path, "/api/") || strings.Contains(u.Path, "/web_api/") || strings.Contains(u.Hostname(), "upload")
}

// declarationJS 文字含「声明」、不含「原创」的最小可见元素（「添加内容类型声明」一类），没有返回 null。
const declarationJS = `() => {
	const visible = e => e.offsetWidth > 0 || e.offsetHeight > 0;
	const hits = [...document.querySelectorAll('body *')].filter(e => {
		const text = (e.innerText || '').trim();
		return visible(e) && text.includes('声明') && !text.includes('原创') && text.length <= 20;
	});
	return hits.find(e => ![...e.children].some(c => hits.includes(c))) || null;
}`

// keywordElements 文字含任一关键词、自己的子元素都不含的元素：祖先路径（标签.class）、文字和外层 HTML（截断）。
func keywordElements(p *rod.Page) []string {
	res, err := p.Eval(`(words) => {
		const out = [];
		const label = e => e.tagName.toLowerCase() + [...e.classList].slice(0, 3).map(c => '.' + c).join('');
		const path = e => { const parts = []; for (let n = e, i = 0; n && n !== document.body && i < 6; n = n.parentElement, i++) parts.unshift(label(n)); return parts.join(' > '); };
		for (const e of document.querySelectorAll('body *')) {
			if (['SCRIPT', 'STYLE'].includes(e.tagName)) continue;
			const text = (e.innerText || '').trim();
			if (!text || text.length > 60 || !words.some(w => text.includes(w))) continue;
			if ([...e.children].some(c => words.some(w => (c.innerText || '').includes(w)))) continue;
			out.push(path(e) + ' | ' + text.replace(/\s+/g, ' ') + ' | ' + e.outerHTML.replace(/\s+/g, ' ').slice(0, 600));
		}
		return out.slice(0, 80);
	}`, inspectKeywords)
	if err != nil {
		return []string{"读取失败: " + err.Error()}
	}
	var out []string
	for _, v := range res.Value.Arr() {
		out = append(out, v.String())
	}
	return out
}

// optionTexts 当前弹出的下拉选项文字。
func optionTexts(p *rod.Page) []string {
	elems, err := p.Elements("div.d-options-wrapper div.d-grid-item")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range elems {
		if text, err := e.Text(); err == nil {
			out = append(out, strings.Join(strings.Fields(text), " "))
		}
	}
	return out
}

func closeOverlay(p *rod.Page) {
	if err := p.Keyboard.Press(input.Escape); err != nil {
		logrus.Debugf("按 Esc 失败: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if hasPopCover(p) {
		clickEmptyPosition(p)
	}
}

// leaveWithoutSaving 离开发布页：弹「是否离开 / 保存草稿」的浏览器对话框就确认离开（不存草稿）。返回弹没弹。
func leaveWithoutSaving(p *rod.Page) bool {
	dialog := make(chan struct{}, 1)
	wait := p.EachEvent(func(e *proto.PageJavascriptDialogOpening) bool {
		_ = proto.PageHandleJavaScriptDialog{Accept: true}.Call(p)
		dialog <- struct{}{}
		return true
	})
	go wait()
	if err := p.Navigate("about:blank"); err != nil {
		logrus.Warnf("离开发布页失败: %v", err)
	}
	select {
	case <-dialog:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}
