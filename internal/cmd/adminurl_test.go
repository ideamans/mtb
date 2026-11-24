package cmd

import (
	"strings"
	"testing"

	"github.com/ideamans/mtb/internal/config"
)

// 管理画面の URL を持つプロファイル（既定のサイト 1）で組む
func adminHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := newHarness(t, opts...)
	f, err := h.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	f.Profiles["prod"].AdminURL = "https://example.com/cgi-bin/mt/mt.cgi"
	f.Profiles["prod"].DefaultSiteID = "1"
	if err := h.store.Save(f); err != nil {
		t.Fatal(err)
	}
	return h
}

// URL の形は MT の管理画面そのもの（object_edit_uri と一覧の __mode=list）。
// mta（MT 9.3）で全部開けることを確かめた形（2026-11-24）。
func TestAdminURLShapes(t *testing.T) {
	h := adminHarness(t)
	const a = "https://example.com/cgi-bin/mt/mt.cgi?"
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"entries", "12"}, a + "__mode=view&_type=entry&blog_id=1&id=12"},
		{[]string{"entry", "12", "--site-id", "3"}, a + "__mode=view&_type=entry&blog_id=3&id=12"},
		{[]string{"entries"}, a + "__mode=list&_type=entry&blog_id=1"},
		{[]string{"pages", "5"}, a + "__mode=view&_type=page&blog_id=1&id=5"},
		{[]string{"templates"}, a + "__mode=list_template&blog_id=1"},
		{[]string{"templates", "7"}, a + "__mode=view&_type=template&blog_id=1&id=7"},
		{[]string{"content-data", "--content-type-id", "3"}, a + "__mode=list&_type=content_data&blog_id=1&type=content_data_3"},
		{[]string{"content-data", "45", "--content-type-id", "3"}, a + "__mode=view&_type=content_data&blog_id=1&content_type_id=3&id=45&type=content_data_3"},
		{[]string{"users", "2"}, a + "__mode=view&_type=author&id=2"},
		{[]string{"users"}, a + "__mode=list&_type=author&blog_id=0"},
		{[]string{"sites"}, a + "__mode=list&_type=website&blog_id=0"},
		{[]string{"sites", "4"}, a + "__mode=dashboard&blog_id=4"},
		{[]string{"settings"}, a + "__mode=cfg_prefs&blog_id=1"},
		{[]string{"logs"}, a + "__mode=list&_type=log&blog_id=1"},
	}
	for _, c := range cases {
		out := h.mustRun(append([]string{"admin-url"}, c.args...)...)
		if got := strings.TrimSpace(out); got != c.want {
			t.Errorf("admin-url %v\n got  %s\n want %s", c.args, got, c.want)
		}
	}
	if len(h.doer.sent) != 0 {
		t.Errorf("通信してはいけない: %d 回", len(h.doer.sent))
	}
}

func TestAdminURLAliasAndErrors(t *testing.T) {
	h := adminHarness(t)
	if out := h.mustRun("management-url", "entries", "1"); !strings.Contains(out, "_type=entry") {
		t.Errorf("別名 management-url: %s", out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"admin-url", "widgets"}, "widgets"},
		{[]string{"admin-url", "entries", "abc"}, "abc"},
		{[]string{"admin-url", "content-data", "1"}, "--content-type-id"},
		{[]string{"admin-url", "settings", "1"}, "ID"},
	} {
		err := h.run(c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v（%q を言うはず）", c.args, err, c.want)
		}
		if ExitCode(err) != ExitUsage {
			t.Errorf("%v: 終了コード %d", c.args, ExitCode(err))
		}
	}
}

func TestAdminURLNeedsSite(t *testing.T) {
	h := newHarness(t) // 既定のサイトなし
	h.mustRun("profile", "set", "--admin-url", "https://example.com/mt.cgi")
	err := h.run("admin-url", "entries", "1")
	if err == nil || !strings.Contains(err.Error(), "--site-id") {
		t.Fatalf("err = %v", err)
	}
}

// 登録が無ければ Data API の URL から推測し、推測したことを標準エラーで言う
func TestAdminURLGuessesAndSaysSo(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("admin-url", "users")
	f, _ := h.store.Load()
	want := config.GuessAdminURL(f.Profiles["prod"].BaseURL)
	if want == "" {
		t.Skip("ハーネスの Data API の URL が mt-data-api.cgi で終わっていない")
	}
	if !strings.HasPrefix(strings.TrimSpace(out), want+"?") {
		t.Errorf("推測した URL を使っていない: %s", out)
	}
	if !strings.Contains(h.errOut.String(), "mtb profile set --admin-url") {
		t.Errorf("推測したことを言っていない: %s", h.errOut.String())
	}
}

func TestAdminURLContentTypesNeedMT7(t *testing.T) {
	h := adminHarness(t)
	h.mustRun("profile", "set", "--api-version", "v3")
	err := h.run("admin-url", "content-types")
	if err == nil || !strings.Contains(err.Error(), "MT 7") {
		t.Fatalf("err = %v", err)
	}
}

// 先に管理画面を聞き、そこから推測した Data API の URL を既定値にして次に聞く
func TestProfileAddAsksAdminURLFirst(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.prompt.NotInteractive = false
	// 管理画面はアドレス欄から貼ったもの、Data API は Enter だけ（推測を採る）
	h.prompt.Lines = []string{"https://new.example.com/cgi-bin/mt/mt.cgi?__mode=dashboard&blog_id=1", ""}
	h.prompt.Confirms = []bool{false}

	h.mustRun("profile", "add", "new", "--api-version", "v7")

	if len(h.prompt.Asked) < 2 || !strings.Contains(h.prompt.Asked[0], "管理画面") || !strings.Contains(h.prompt.Asked[1], "Data API") {
		t.Fatalf("聞く順番: %v", h.prompt.Asked)
	}
	f, _ := h.store.Load()
	p := f.Profiles["new"]
	if p.AdminURL != "https://new.example.com/cgi-bin/mt/mt.cgi" {
		t.Errorf("管理画面の URL（問い合わせは落とす）: %q", p.AdminURL)
	}
	if p.BaseURL != "https://new.example.com/cgi-bin/mt/mt-data-api.cgi" {
		t.Errorf("推測した Data API の URL が既定値になっていない: %q", p.BaseURL)
	}
}

// 管理画面を聞いたのに Data API の URL を入れたら、それを Data API の既定値に回す
func TestProfileAddAdminPromptGivenDataAPIURL(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.prompt.NotInteractive = false
	h.prompt.Lines = []string{"https://new.example.com/mt/mt-data-api.cgi", ""}
	h.prompt.Confirms = []bool{false}

	h.mustRun("profile", "add", "new", "--api-version", "v7")

	f, _ := h.store.Load()
	p := f.Profiles["new"]
	if p.BaseURL != "https://new.example.com/mt/mt-data-api.cgi" || p.AdminURL != "https://new.example.com/mt/mt.cgi" {
		t.Errorf("取り違えを直していない: %+v", p)
	}
}

// 対話できない場面で --admin-url だけなら、推測した Data API の URL で登録し、そう言う
func TestProfileAddAdminURLOnly(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.mustRun("profile", "add", "new", "--admin-url", "https://new.example.com/mt/admin", "--api-version", "v7")
	f, _ := h.store.Load()
	if p := f.Profiles["new"]; p.BaseURL != "https://new.example.com/mt/mt-data-api.cgi" || p.AdminURL != "https://new.example.com/mt/admin" {
		t.Errorf("%+v", p)
	}
	if !strings.Contains(h.errOut.String(), "推測") {
		t.Errorf("推測したことを言っていない: %s", h.errOut.String())
	}
}

func TestProfileSetAndShowAdminURL(t *testing.T) {
	h := newHarness(t)
	if out := h.mustRun("profile", "show"); !strings.Contains(out, "管理画面") {
		t.Errorf("show に管理画面の行が無い: %s", out)
	}
	h.mustRun("profile", "set", "--admin-url", "https://example.com/mt/admin?__mode=dashboard")
	if out := h.mustRun("profile", "show"); !strings.Contains(out, "https://example.com/mt/admin\n") {
		t.Errorf("登録した URL が出ない: %s", out)
	}
	h.mustRun("profile", "set", "--admin-url", "")
	f, _ := h.store.Load()
	if f.Profiles["prod"].AdminURL != "" {
		t.Errorf("空で消えない: %q", f.Profiles["prod"].AdminURL)
	}
}
