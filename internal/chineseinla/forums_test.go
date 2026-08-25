package chineseinla

import (
	"strings"
	"testing"
)

func TestParseForums(t *testing.T) {
	t.Parallel()

	htmlSource := `<!doctype html><html><body>
		<a href="/f/page_pppping/mode_newtopic/f_130.html">品牌合作专区</a>
		<a href="/f/c_10.html">生活服务</a>
		<div><img alt="找工作与招聘"><a href="/f/page_pppping/mode_newtopic/f_21.html">工作求职</a></div>
		<a href="/f/page_pppping/mode_newtopic/f_21.html">重复链接</a>
		<a href="/f/c_11.html">合作专区</a>
		<div><a href="/f/page_pppping/mode_newtopic/f_99.html">认证商家专用</a></div>
	</body></html>`

	forums, err := ParseForums(strings.NewReader(htmlSource))
	if err != nil {
		t.Fatalf("ParseForums: %v", err)
	}
	if len(forums) != 3 {
		t.Fatalf("forum count = %d, want 3: %#v", len(forums), forums)
	}
	if !forums[0].Restricted {
		t.Fatal("ungrouped branded forum was not marked restricted")
	}
	if forums[1].ID != 21 || forums[1].Name != "工作求职" || forums[1].Group != "生活服务" {
		t.Fatalf("unexpected ordinary forum: %#v", forums[1])
	}
	if forums[1].Description != "找工作与招聘" {
		t.Fatalf("description = %q, want image alt", forums[1].Description)
	}
	if forums[1].Restricted {
		t.Fatal("ordinary forum was marked restricted")
	}
	if !forums[2].Restricted {
		t.Fatal("restricted forum was not marked restricted")
	}
}

func TestParseForumsRequiresPostLinks(t *testing.T) {
	t.Parallel()
	if _, err := ParseForums(strings.NewReader(`<a href="/f/c_1.html">Only a group</a>`)); err == nil {
		t.Fatal("expected missing forum links to fail")
	}
}
