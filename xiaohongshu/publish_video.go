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

	// 重设超时：.Context(ctx) 会替换掉 NewPublishVideoAction 里 Timeout(300s) 的 deadline
	page := p.page.Context(ctx).Timeout(300 * time.Second)
	defer func() {
		if err != nil {
			p.trace.Capture(page, "publish_failed")
		}
	}()

	if err := uploadVideo(page, content.VideoPath); err != nil {
		return errors.Wrap(err, "小红书上传视频失败")
	}
	p.trace.Capture(page, "video_uploaded")

	// A requested cover must either be applied or fail the publish: silently
	// falling back to Xiaohongshu's first frame would ship a note whose cover
	// is not the one that was asked for.
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

// setVideoCover 用本地图片替换默认首帧封面。
//
// 创作中心的封面控件：点"编辑封面"(.cover-edit-entry) 打开 d-modal，模态里有
// "截取封面"/"上传封面" 两个 tab，切到"上传封面"后由其中的 file input 接收图片，
// 最后点页脚的"确定"(.btn-confirm) 应用。每一步都显式校验，失败即报错。
func setVideoCover(page *rod.Page, coverPath string) error {
	if _, err := os.Stat(coverPath); err != nil {
		return errors.Wrapf(err, "封面文件不存在或不可访问: %s", coverPath)
	}

	pp := page.Timeout(2 * time.Minute)

	entry, err := pp.Element(".cover-edit-entry")
	if err != nil || entry == nil {
		return errors.New("未找到\"编辑封面\"入口")
	}
	if err := entry.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击\"编辑封面\"失败")
	}

	// 等模态出现（页脚的确定按钮是它就绪的标志）
	if err := waitForSelector(pp, ".d-modal-footer .btn-confirm", 30*time.Second); err != nil {
		return errors.Wrap(err, "封面编辑弹窗未打开")
	}

	// 切到"上传封面"
	switched, err := pp.Eval(`() => {
	  const t = [...document.querySelectorAll('.d-tabs-header')].find(
	    e => (e.textContent||'').trim() === '上传封面');
	  if (!t) return false;
	  t.click();
	  return true;
	}`)
	if err != nil || !switched.Value.Bool() {
		return errors.New("未找到\"上传封面\"标签页")
	}
	time.Sleep(2 * time.Second)

	// 弹窗里接收图片的 input：按 accept 认图片，排除外层那个只收视频的 .upload-input
	input, err := findCoverFileInput(pp, 20*time.Second)
	if err != nil {
		return err
	}
	if err := input.SetFiles([]string{coverPath}); err != nil {
		return errors.Wrap(err, "提交封面图片失败")
	}

	// 图片进裁剪器后"确定"才有意义：上传成功时 .center-box 由 display:none 变为可见，
	// cropperjs 也会建出 .cropper-container。任一出现即认为图片已就位。
	deadlineCrop := time.Now().Add(60 * time.Second)
	loaded := false
	for time.Now().Before(deadlineCrop) {
		ok, err := pp.Eval(`() => {
		  const box = document.querySelector('.d-tabs-pane[name=uploadTab] .center-box');
		  const boxShown = !!box && box.offsetParent !== null;
		  const cropper = !!document.querySelector('.d-tabs-pane[name=uploadTab] .cropper-container');
		  return boxShown || cropper;
		}`)
		if err == nil && ok.Value.Bool() {
			loaded = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !loaded {
		return errors.New("封面图片未加载到裁剪器")
	}
	time.Sleep(2 * time.Second)

	confirm, err := pp.Element(".d-modal-footer .btn-confirm")
	if err != nil || confirm == nil {
		return errors.New("未找到封面弹窗的\"确定\"按钮")
	}
	if err := confirm.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击封面\"确定\"失败")
	}

	// 弹窗关闭才算应用成功
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		open, err := pp.Eval(`() => !!document.querySelector('.d-modal-footer .btn-confirm')`)
		if err == nil && !open.Value.Bool() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("点击\"确定\"后封面弹窗未关闭，封面可能未应用")
}

// findCoverFileInput 找弹窗里那个收图片的 file input。
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
				// accept 没写全时，退而求其次：不是外层收视频的那个就用它
				class, _ := el.Attribute("class")
				if accept == nil && (class == nil || !strings.Contains(*class, "upload-input")) {
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
