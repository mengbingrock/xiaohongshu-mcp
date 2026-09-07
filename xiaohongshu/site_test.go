package xiaohongshu

import "testing"

func TestSiteByKey(t *testing.T) {
	if SiteByKey("intl").Key != "intl" {
		t.Fatalf("intl key should resolve to INTL site")
	}
	if SiteByKey("cn").Key != "cn" {
		t.Fatalf("cn key should resolve to CN site")
	}
	// Unknown keys default to CN so a corrupt/legacy value never routes INTL.
	if SiteByKey("").Key != "cn" || SiteByKey("bogus").Key != "cn" {
		t.Fatalf("unknown key should default to CN")
	}
}

func TestSiteURLsUnchangedForCN(t *testing.T) {
	// Guards the Phase-1 promise: the CN path is byte-for-byte unchanged.
	if SiteCN.HomeURL != "https://www.xiaohongshu.com/explore" {
		t.Fatalf("CN HomeURL changed: %s", SiteCN.HomeURL)
	}
	if SiteCN.PublishURL != "https://creator.xiaohongshu.com/publish/publish?source=official" {
		t.Fatalf("CN PublishURL changed: %s", SiteCN.PublishURL)
	}
	if CurrentSite().Key != "cn" {
		t.Fatalf("default site must be CN, got %s", CurrentSite().Key)
	}
}

func TestResolveSiteFromCookies(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"nil", "", "cn"},
		{"garbage", "not json", "cn"},
		{"cn session", `[{"name":"web_session","domain":".xiaohongshu.com"}]`, "cn"},
		{"intl id_token", `[{"name":"web_session","domain":".xiaohongshu.com"},{"name":"id_token","domain":".rednote.com"}]`, "intl"},
		{"rednote cookies without id_token", `[{"name":"web_session","domain":".rednote.com"}]`, "cn"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b []byte
			if c.data != "" {
				b = []byte(c.data)
			}
			if got := ResolveSiteFromCookies(b).Key; got != c.want {
				t.Fatalf("want %s got %s", c.want, got)
			}
		})
	}
}

func TestSetSiteRestore(t *testing.T) {
	orig := CurrentSite()
	defer SetSite(orig)
	SetSite(SiteINTL)
	if CurrentSite().Key != "intl" {
		t.Fatalf("SetSite(INTL) not reflected")
	}
}
