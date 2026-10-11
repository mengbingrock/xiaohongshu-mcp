package xiaohongshu

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/site"
)

// Site holds everything that differs between Xiaohongshu's domestic property
// (xiaohongshu.com) and its international brand (rednote.com). Both are served
// by the same creator-center SPA in Chinese, so only hosts/URLs differ — the
// DOM selectors are shared (verified Phase 0). Read-only feed/notification
// navigations elsewhere in this package stay CN-only; the Postiz path uses only
// login, the login checks, and publish, which are the flows parameterised here.
type Site struct {
	Key          string // "cn" | "intl"
	HomeURL      string // navigate here to trigger the QR modal and to check the www session
	PublishURL   string // creator-center publish page
	CreatorHost  string // host used in URL substring checks
	CookieDomain string // primary session cookie domain
}

var (
	SiteCN = Site{
		Key:          "cn",
		HomeURL:      "https://www.xiaohongshu.com/explore",
		PublishURL:   "https://creator.xiaohongshu.com/publish/publish?source=official",
		CreatorHost:  "creator.xiaohongshu.com",
		CookieDomain: ".xiaohongshu.com",
	}
	// SiteINTL: www.rednote.com/explore 301s to /, so HomeURL is the root.
	SiteINTL = Site{
		Key:          "intl",
		HomeURL:      "https://www.rednote.com/",
		PublishURL:   "https://creator.rednote.com/publish/publish?source=official",
		CreatorHost:  "creator.rednote.com",
		CookieDomain: ".rednote.com",
	}
)

// SiteByKey returns the site for a stored key, defaulting to CN when unknown.
// "rednote" is what older ycl-2004 builds stamped on INTL session files.
func SiteByKey(key string) Site {
	switch key {
	case SiteINTL.Key, "rednote":
		return SiteINTL
	default:
		return SiteCN
	}
}

var (
	siteMu      sync.RWMutex
	currentSite = SiteCN
)

// SetSite selects the site subsequent navigations use, and keeps the site
// package (used by search, feeds, notifications, chat) in step. A process
// pinned by XHS_SITE never switches: a login that lands on the other site is
// logged and ignored. Modified for ycl-2004/xiaohongshu-mcp; see NOTICE.
func SetSite(s Site) {
	if site.Pinned() && !site.SameSite(site.Current().Key, s.Key) {
		logrus.Warnf("站点钉在 %s（XHS_SITE），不切到 %s", site.Current().Name, s.Key)
		return
	}
	siteMu.Lock()
	currentSite = s
	siteMu.Unlock()
	if ps, err := site.Parse(s.Key); err == nil {
		site.Set(ps)
	}
}

// CurrentSite returns the active site (CN by default).
func CurrentSite() Site {
	siteMu.RLock()
	defer siteMu.RUnlock()
	return currentSite
}

// ResolveSiteFromCookies picks the site a saved session belongs to. An
// international session carries an id_token on a rednote.com domain; every other
// (or unreadable) session is treated as CN. Input is the raw cookie array as
// returned by cookies.LoadCookies.
func ResolveSiteFromCookies(data []byte) Site {
	if len(data) == 0 {
		return SiteCN
	}
	var cks []struct {
		Name   string `json:"name"`
		Domain string `json:"domain"`
	}
	if json.Unmarshal(data, &cks) != nil {
		return SiteCN
	}
	for _, c := range cks {
		if c.Name == "id_token" && strings.Contains(c.Domain, "rednote.com") {
			return SiteINTL
		}
	}
	return SiteCN
}

// ResolveSite decides the active site for a session file, in priority order:
// an explicit env override (XHS_SITE), the site stamped at the last login,
// then a classification of the saved cookies, then CN. Mirrors the seed
// resolution so a deployment pinned by env is never overridden by the file.
func ResolveSite(envKey string, store cookies.Cookier) Site {
	if envKey == SiteCN.Key || envKey == SiteINTL.Key || envKey == "rednote" {
		return SiteByKey(envKey)
	}
	if store == nil {
		return SiteCN
	}
	if key := store.LoadSite(); key != "" {
		return SiteByKey(key)
	}
	if data, err := store.LoadCookies(); err == nil {
		return ResolveSiteFromCookies(data)
	}
	return SiteCN
}

// SiteForFacts classifies where a login just landed from the browser's
// cookies: an international session carries an id_token on rednote.com.
func SiteForFacts(f SessionFacts) Site {
	if f.RednoteIDToken {
		return SiteINTL
	}
	return SiteCN
}
