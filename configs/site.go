package configs

import (
	"os"
	"strings"

	"github.com/xpzouying/xiaohongshu-mcp/site"
)

// UseSiteFromEnv 设了 XHS_SITE（cn、rednote 或 intl）就把本进程钉在这个站点上：
// 只服务它，另一个站点的会话拒绝加载、不覆盖，登录落到别的站点也不切过去。
// 没设时返回 pinned=false，站点由 xiaohongshu.ResolveSite 按会话文件定。
// 不认识的值返回错误，入口层据此拒绝启动。env 读取集中在配置层。
// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.
func UseSiteFromEnv() (s site.Site, pinned bool, err error) {
	name := strings.TrimSpace(os.Getenv(site.EnvName))
	if name == "" {
		return site.CN, false, nil
	}
	s, err = site.Parse(name)
	if err != nil {
		return site.Site{}, false, err
	}
	site.Pin(s)
	return s, true, nil
}
