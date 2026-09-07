package xiaohongshu

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/xpzouying/xiaohongshu-mcp/cookies"
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
func SiteByKey(key string) Site {
	switch key {
	case SiteINTL.Key:
		return SiteINTL
	default:
		return SiteCN
	}
}

var (
	siteMu      sync.RWMutex
	currentSite = SiteCN
)

// SetSite selects the site subsequent login/publish navigations use. Nothing
// calls it yet in Phase 1, so the default (CN) keeps every URL unchanged.
func SetSite(s Site) {
	siteMu.Lock()
	currentSite = s
	siteMu.Unlock()
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
	if envKey == SiteCN.Key || envKey == SiteINTL.Key {
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
