package xiaohongshu

import (
	"errors"
	"testing"
)

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

type fakeStore struct {
	site    string
	cookies []byte
	loadErr error
}

func (f *fakeStore) LoadCookies() ([]byte, error) { return f.cookies, f.loadErr }
func (f *fakeStore) SaveCookies([]byte) error     { return nil }
func (f *fakeStore) DeleteCookies() error         { return nil }
func (f *fakeStore) LoadSeed() int                { return 0 }
func (f *fakeStore) SaveSeed(int) error           { return nil }
func (f *fakeStore) LoadSite() string             { return f.site }
func (f *fakeStore) SaveSite(s string) error      { f.site = s; return nil }

func TestResolveSitePriority(t *testing.T) {
	intlCookies := []byte(`[{"name":"id_token","domain":".rednote.com"}]`)

	// env wins over everything
	if got := ResolveSite("cn", &fakeStore{site: "intl", cookies: intlCookies}).Key; got != "cn" {
		t.Fatalf("env should win, got %s", got)
	}
	// stored key wins over cookie classification
	if got := ResolveSite("", &fakeStore{site: "cn", cookies: intlCookies}).Key; got != "cn" {
		t.Fatalf("stored key should win over cookies, got %s", got)
	}
	// no stored key: classify cookies
	if got := ResolveSite("", &fakeStore{cookies: intlCookies}).Key; got != "intl" {
		t.Fatalf("cookies should classify INTL, got %s", got)
	}
	// unreadable file: CN
	if got := ResolveSite("", &fakeStore{loadErr: errors.New("nope")}).Key; got != "cn" {
		t.Fatalf("unreadable store should default CN, got %s", got)
	}
	// bogus env key is ignored, not treated as CN override
	if got := ResolveSite("bogus", &fakeStore{site: "intl"}).Key; got != "intl" {
		t.Fatalf("bogus env must be ignored, got %s", got)
	}
}

func TestSiteForFacts(t *testing.T) {
	if SiteForFacts(SessionFacts{RednoteIDToken: true}).Key != "intl" {
		t.Fatal("id_token should mean INTL")
	}
	if SiteForFacts(SessionFacts{}).Key != "cn" {
		t.Fatal("no id_token should mean CN")
	}
}
