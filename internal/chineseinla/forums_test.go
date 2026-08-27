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
	if forums[1].ID != 21 || forums[1].Name != "工作求职" || forums[1].GroupID != 10 || forums[1].Group != "生活服务" {
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

func TestParseForumsMatchesLiveSelectorStructure(t *testing.T) {
	t.Parallel()

	htmlSource := `<!doctype html><html><body>
		<div class="forum_group_select">
			<div class="category_select"><a href="/f/c_2/sid_redacted.html">休闲娱乐</a></div>
			<div class="special_column_select">
				<div class="forum_img"><img title="笑话大世界"></div>
				<div class="forum_title"><span><a href="/f/page_pppping/mode_newtopic/f_23.html">轻松一刻</a></span></div>
			</div>
		</div>
		<div class="forum_group_select">
			<div class="category_select"><a href="/f/c_13/sid_redacted.html">洛城汽车</a></div>
			<div class="special_column_select">
				<div class="forum_img"><img title="仅限Car Dealer发布卖车信息"></div>
				<div class="forum_title"><a href="/f/page_pppping/mode_newtopic/f_95.html">车商卖车</a></div>
			</div>
		</div>
	</body></html>`

	forums, err := ParseForums(strings.NewReader(htmlSource))
	if err != nil {
		t.Fatalf("ParseForums: %v", err)
	}
	if len(forums) != 2 {
		t.Fatalf("forum count = %d, want 2: %#v", len(forums), forums)
	}
	if forums[0].ID != 23 || forums[0].GroupID != 2 || forums[0].Group != "休闲娱乐" {
		t.Fatalf("unexpected live forum: %#v", forums[0])
	}
	if forums[0].Description != "笑话大世界" || forums[0].Restricted {
		t.Fatalf("unexpected live forum metadata: %#v", forums[0])
	}
	if !forums[1].Restricted {
		t.Fatalf("description restriction was not detected: %#v", forums[1])
	}
}

func TestParseForumsRequiresPostLinks(t *testing.T) {
	t.Parallel()
	if _, err := ParseForums(strings.NewReader(`<a href="/f/c_1.html">Only a group</a>`)); err == nil {
		t.Fatal("expected missing forum links to fail")
	}
}
