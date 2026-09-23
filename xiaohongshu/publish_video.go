package xiaohongshu

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// PublishVideoContent 发布视频内容
type PublishVideoContent struct {
	Title        string
	Content      string
	Tags         []string
	VideoPath    string
	CoverPath    string     // 自定义封面本地图片，为空则沿用小红书默认首帧
	ScheduleTime *time.Time // 定时发布时间，nil 表示立即发布
	Visibility   string     // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products     []string   // 商品关键词列表，用于绑定带货商品
}

// NewPublishVideoAction 进入发布页并切换到"上传视频"
func NewPublishVideoAction(page *rod.Page) (*PublishAction, error) {
	trace := newPublishTrace("video")
	pp := page.Timeout(300 * time.Second)
	trace.AttachNetwork(pp)

	if err := pp.Navigate(CurrentSite().PublishURL); err != nil {
		trace.Capture(pp, "navigation_failed")
		return nil, errors.Wrap(err, "导航到发布页面失败")
	}
	trace.Capture(pp, "publish_page_navigated")

	// 使用 WaitLoad 代替 WaitIdle（更宽松）
	if err := pp.WaitLoad(); err != nil {
		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
	}
	time.Sleep(2 * time.Second)

	if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
	}
	time.Sleep(1 * time.Second)

	if err := mustClickPublishTab(pp, "上传视频"); err != nil {
		trace.Capture(pp, "video_tab_failed")
		return nil, errors.Wrap(err, "切换到上传视频失败")
	}
	trace.Capture(pp, "video_tab_ready")

	time.Sleep(1 * time.Second)
	if err := checkCreatorSession(pp); err != nil {
		trace.Capture(pp, "creator_session_expired")
		return nil, trace.Annotate(err)
	}

	return &PublishAction{page: pp, trace: trace}, nil
}

// PublishVideo 上传视频并提交
func (p *PublishAction) PublishVideo(ctx context.Context, content PublishVideoContent) (err error) {
	if content.VideoPath == "" {
		return errors.New("视频不能为空")
	}

	// 重设超时：.Context(ctx) 会替换掉 NewPublishVideoAction 里 Timeout(300s) 的 deadline。
	// 带自定义封面时要多等编辑器把视频加载完，整体放宽。
	overall := 300 * time.Second
	if content.CoverPath != "" {
		overall = 10 * time.Minute
	}
	page := p.page.Context(ctx).Timeout(overall)
	defer func() {
		if err != nil {
			p.trace.Capture(page, "publish_failed")
		}
	}()

	if err := uploadVideo(page, content.VideoPath); err != nil {
		return errors.Wrap(err, "小红书上传视频失败")
	}
	p.trace.Capture(page, "video_uploaded")

	// 要了封面就必须设上，设不上直接让发布失败：悄悄退回小红书的首帧，
	// 发出去的笔记封面就不是用户要的那张了。
	if content.CoverPath != "" {
		if err := setVideoCover(page, content.CoverPath); err != nil {
			p.trace.Capture(page, "cover_failed")
			return errors.Wrap(err, "设置视频封面失败")
		}
		p.trace.Capture(page, "cover_set")
	}

	if err := submitPublishVideo(ctx, page, p.trace, content.Title, content.Content, content.Tags, content.ScheduleTime, content.Visibility, content.Products); err != nil {
		return errors.Wrap(err, "小红书发布失败")
	}
	p.trace.Capture(page, "publish_completed")
	return nil
}

// uploadVideo 上传单个本地视频
func uploadVideo(page *rod.Page, videoPath string) error {
	pp := page.Timeout(5 * time.Minute) // 视频处理耗时更长

	if _, err := os.Stat(videoPath); os.IsNotExist(err) {
		return errors.Wrapf(err, "视频文件不存在: %s", videoPath)
	}

	// 寻找文件上传输入框（与图文一致的 class，或退回到 input[type=file]）
	var fileInput *rod.Element
	var err error
	fileInput, err = pp.Element(".upload-input")
	if err != nil || fileInput == nil {
		fileInput, err = pp.Element("input[type='file']")
		if err != nil || fileInput == nil {
			return errors.New("未找到视频上传输入框")
		}
	}

	fileInput.MustSetFiles(videoPath)

	// 对于视频，等待发布按钮变为可点击即表示处理完成
	btn, err := waitForPublishButtonClickable(pp, 10*time.Minute)
	if err != nil {
		return err
	}
	slog.Info("视频上传/处理完成，发布按钮可点击", "btn", btn)
	return nil
}

const (
	// coverModalSelector 封面编辑弹窗，它没有 d-modal-footer，按钮都在内容区里
	coverModalSelector = ".main-cover-editor-modal"
	// uploadedCoverSelector 图片上传成功后，"上传"槽位会变成这个缩略图按钮
	uploadedCoverSelector = "button.uploaded-thumbnail"
)

// setVideoCover 用本地图片替换默认首帧封面。
//
// 创作中心的封面控件：hover 封面预览区让"编辑封面"(.cover-edit-entry) 显示出来，
// 点开后是封面编辑器弹窗，弹窗底部"上传"按钮后面挂着只收图片的 file input，
// 最后点右下角"完成"应用。每一步都限时并显式校验，失败即报错。
func setVideoCover(page *rod.Page, coverPath string) error {
	if _, err := os.Stat(coverPath); err != nil {
		return errors.Wrapf(err, "封面文件不存在或不可访问: %s", coverPath)
	}

	pp := page.Timeout(8 * time.Minute)

	// "编辑封面"一直在 DOM 里，但要 hover 封面预览区才显示出来
	entry, err := pp.Timeout(30 * time.Second).Element(".cover-edit-entry")
	if err != nil || entry == nil {
		return errors.New("未找到\"编辑封面\"入口")
	}
	if err := hoverCoverPreview(pp, entry); err != nil {
		return err
	}
	if err := entry.Timeout(20 * time.Second).WaitVisible(); err != nil {
		return errors.Wrap(err, "\"编辑封面\"按钮未显示")
	}
	if err := entry.Timeout(20*time.Second).Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击\"编辑封面\"失败")
	}

	if err := waitForSelector(pp, coverModalSelector, 30*time.Second); err != nil {
		return errors.Wrap(err, "封面编辑弹窗未打开")
	}

	// 弹窗里接收图片的 input：按 accept 认图片，排除外层那个只收视频的同名 input
	input, err := findCoverFileInput(pp, 20*time.Second)
	if err != nil {
		return err
	}
	if err := input.SetFiles([]string{coverPath}); err != nil {
		return errors.Wrap(err, "提交封面图片失败")
	}

	// 上传成功后"上传"位置会变成一张已上传封面的缩略图，点它选中作为封面
	if err := waitForSelector(pp, uploadedCoverSelector, 2*time.Minute); err != nil {
		return errors.Wrap(err, "封面图片未上传成功")
	}
	thumb, err := pp.Timeout(20 * time.Second).Element(uploadedCoverSelector)
	if err != nil || thumb == nil {
		return errors.New("未找到已上传的封面缩略图")
	}
	if err := thumb.Timeout(20*time.Second).Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "选中已上传封面失败")
	}

	// 视频在弹窗里还没加载完时"完成"是禁用的，等它可点再点
	done, err := waitCoverDoneEnabled(pp, 3*time.Minute)
	if err != nil {
		return err
	}
	if err := done.Timeout(20*time.Second).Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击封面\"完成\"失败")
	}

	// 弹窗关闭才算应用成功
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		has, _, err := pp.Has(coverModalSelector)
		if err == nil && !has {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("点击\"完成\"后封面弹窗未关闭，封面可能未应用")
}

// hoverCoverPreview 把"编辑封面"浮层 hover 出来。
//
// 浮层本身不可交互，直接 Click 会卡在 rod 的 WaitInteractable 上直到页面超时，
// 所以从它往上找第一个能 hover 的祖先容器（即封面预览区）。
func hoverCoverPreview(page *rod.Page, entry *rod.Element) error {
	// 封面预览区就是那张默认首帧缩略图
	if preview, err := page.Timeout(5 * time.Second).Element(".default.row"); err == nil && preview != nil {
		if err := preview.Timeout(5 * time.Second).Hover(); err == nil {
			return nil
		}
	}

	// 兜底：从浮层往上找第一个能 hover 的祖先。只找三层，避免 hover 到整个版块
	// 上去——那样鼠标落在缩略图之外，浮层同样不会出现。
	node := entry
	for i := 0; i < 3; i++ {
		parent, err := node.Parent()
		if err != nil || parent == nil {
			break
		}
		node = parent
		if err := node.Timeout(5 * time.Second).Hover(); err == nil {
			return nil
		}
	}
	return errors.New("无法 hover 封面预览区，\"编辑封面\"不会显示")
}

// waitCoverDoneEnabled 等封面弹窗的"完成"按钮变为可点。
//
// 视频要先在弹窗里加载完，按钮才从 disabled 变回可点；禁用态下 pointer-events
// 是 none，直接点会被 rod 挡下来。
func waitCoverDoneEnabled(page *rod.Page, timeout time.Duration) (*rod.Element, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		done, err := page.Timeout(10*time.Second).ElementR(coverModalSelector+" button", "完成")
		if err == nil && done != nil {
			class, cerr := done.Attribute("class")
			if cerr == nil && (class == nil || !strings.Contains(*class, "disabled")) {
				return done, nil
			}
		}
		time.Sleep(time.Second)
	}
	return nil, errors.New("封面弹窗的\"完成\"按钮一直不可点，视频可能没在弹窗里加载完")
}

// findCoverFileInput 找弹窗里那个收图片的 file input。
//
// 页面上有两个 .upload-input：外层收视频，弹窗里这个收图片，靠 accept 区分。
func findCoverFileInput(page *rod.Page, timeout time.Duration) (*rod.Element, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		els, err := page.Elements("input[type='file']")
		if err == nil {
			for _, el := range els {
				accept, _ := el.Attribute("accept")
				if accept != nil && strings.Contains(strings.ToLower(*accept), "image") {
					return el, nil
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil, errors.New("封面弹窗里未找到图片上传输入框")
}

func waitForSelector(page *rod.Page, selector string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		has, _, err := page.Has(selector)
		if err == nil && has {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.Errorf("等待元素超时: %s", selector)
}

// submitPublishVideo 填写标题、正文、标签并点击发布（等待按钮可点击后再提交）
func submitPublishVideo(ctx context.Context, page *rod.Page, trace *publishTrace, title, content string, tags []string, scheduleTime *time.Time, visibility string, products []string) error {
	// 标题
	titleElem, err := getTitleElement(page, titleElemTimeout)
	if err != nil {
		return errors.Wrap(err, "查找标题输入框失败（"+publishPageSnapshot(page)+"）")
	}
	if err := humanize.Type(ctx, titleElem, title); err != nil {
		return errors.Wrap(err, "输入标题失败")
	}
	trace.Capture(page, "title_filled")
	humanize.Delay(ctx, humanize.AfterType)

	// 正文 + 标签
	contentElem, err := getContentElement(page, contentElemTimeout)
	if err != nil {
		return err
	}
	if err := humanize.Type(ctx, contentElem, content); err != nil {
		return errors.Wrap(err, "输入正文失败")
	}
	trace.Capture(page, "content_filled")
	if err := waitAndClickTitleInput(titleElem); err != nil {
		return err
	}
	if err := inputTags(ctx, contentElem, tags); err != nil {
		return err
	}
	trace.Capture(page, "tags_filled")

	humanize.Delay(ctx, humanize.AfterType)

	// 处理定时发布
	if scheduleTime != nil {
		if err := setSchedulePublish(ctx, page, *scheduleTime); err != nil {
			return errors.Wrap(err, "设置定时发布失败")
		}
		slog.Info("定时发布设置完成", "schedule_time", scheduleTime.Format("2006-01-02 15:04"))
		trace.Capture(page, "schedule_set")
	}

	// 设置可见范围
	if err := setVisibility(page, visibility); err != nil {
		return errors.Wrap(err, "设置可见范围失败")
	}
	trace.Capture(page, "visibility_set")

	// 绑定商品
	if err := bindProducts(ctx, page, products); err != nil {
		return errors.Wrap(err, "绑定商品失败")
	}
	trace.Capture(page, "products_processed")

	trace.Capture(page, "before_publish_click")
	if err := clickPublishButton(page); err != nil {
		return err
	}
	trace.Capture(page, "publish_clicked")

	// 校验发布真的成功（成功跳转离开发布页），未跳转判失败——消除假成功
	if err := waitPublishSuccess(page, trace, 15*time.Second); err != nil {
		return err
	}
	trace.Capture(page, "publish_success_confirmed")
	return nil
}
