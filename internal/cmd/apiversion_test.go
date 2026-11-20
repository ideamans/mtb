package cmd

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ideamans/mtb/internal/spec"
)

// withAPIVersion は既定のプロファイル（prod）の版を差し替えます。
// 版はプロファイルにだけ持たせるので、テストでもそこを書き換えます。
func (h *harness) withAPIVersion(v string) *harness {
	h.t.Helper()
	f, err := h.store.Load()
	if err != nil {
		h.t.Fatal(err)
	}
	f.Profiles[f.Current].APIVersion = v
	if err := h.store.Save(f); err != nil {
		h.t.Fatal(err)
	}
	return h
}

// v4 からの操作（コンテンツタイプ）を v3 の接続先に送らないこと。
// 送ると MT は 404 を返し、「対象が無い」と区別が付かない。
func TestOperationNewerThanTargetIsNotSent(t *testing.T) {
	h := newHarness(t).withAPIVersion("v3")
	err := h.run("sites", "content-types", "list", "1")
	if err == nil {
		t.Fatal("v3 の接続先にコンテンツタイプの一覧を送りました")
	}
	if ExitCode(err) != ExitUsage {
		t.Errorf("終了コード = %d", ExitCode(err))
	}
	if !strings.Contains(err.Error(), "v4") || !strings.Contains(err.Error(), "v3") {
		t.Errorf("理由が分かりません: %v", err)
	}
	if len(h.doer.sent) != 0 {
		t.Errorf("送信されました: %v", h.doer.sent)
	}
}

func TestDryRunAlsoStopsOnVersion(t *testing.T) {
	h := newHarness(t).withAPIVersion("v3")
	if err := h.run("--dry-run", "sites", "content-types", "list", "1"); err == nil {
		t.Fatal("--dry-run で版の足りない操作を通しました")
	}
}

func TestOperationWithinVersionIsSentToVersionedPath(t *testing.T) {
	h := newHarness(t).withAPIVersion("v3").reply(okJSON(`{"totalResults":0,"items":[]}`))
	h.mustRun("sites", "entries", "list", "1")
	if got := pathOf(t, h.last().URL); got != "/mt/mt-data-api.cgi/v3/sites/1/entries" {
		t.Errorf("path = %s", got)
	}
}

// v1 にあるのは記事・コメントなど一部だけ。ウェブページは v2 から。
func TestV1LacksPages(t *testing.T) {
	h := newHarness(t).withAPIVersion("v1")
	if err := h.run("sites", "pages", "list", "1"); err == nil {
		t.Fatal("v1 にウェブページの一覧を送りました")
	}
	h.reply(okJSON(`{"totalResults":0,"items":[]}`))
	h.mustRun("sites", "entries", "list", "1")
}

func TestInvalidAPIVersionIsRejected(t *testing.T) {
	for _, v := range []string{"7", "v0", "vx", "latest"} {
		h := newHarness(t).withAPIVersion(v)
		err := h.run("sites", "entries", "list", "1")
		if err == nil || ExitCode(err) != ExitUsage {
			t.Errorf("%q: err = %v", v, err)
		}
		if len(h.doer.sent) != 0 {
			t.Errorf("%q: 送信されました", v)
		}
	}
}

func TestTemplatePullNeedsV2(t *testing.T) {
	h := newHarness(t).withAPIVersion("v1")
	err := h.run("sites", "templates", "pull", "1", "--dir", "tpl")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("err = %v", err)
	}
	if len(h.doer.sent) != 0 {
		t.Errorf("送信されました: %v", h.doer.sent)
	}
}

// v1・v2 の /authentication はサインインのパスワードを受け取る。
// 聞くときの名前を違えると、利用者は Webサービスパスワードを入れて弾かれる。
func TestAuthLoginOnV2AsksForSignInPassword(t *testing.T) {
	h := newHarness(t).withAPIVersion("v2")
	h.prompt.NotInteractive = false
	h.prompt.Lines = []string{"bob"}
	h.prompt.Secrets = []string{"pw"}
	h.reply(okJSON(`{"accessToken":"t","sessionId":"s","expiresIn":3600}`))
	h.mustRun("auth", "login")

	if contains(h.prompt.Asked, "Webサービスパスワード") {
		t.Errorf("v2 で Webサービスパスワードを聞きました: %v", h.prompt.Asked)
	}
	if got := pathOf(t, h.last().URL); got != "/mt/mt-data-api.cgi/v2/authentication" {
		t.Errorf("path = %s", got)
	}
}

func TestAuthLoginFailureOnV2PointsToSignInPassword(t *testing.T) {
	h := newHarness(t).withAPIVersion("v2")
	h.reply(stub{status: http.StatusUnauthorized, body: `{"error":{"code":401,"message":"Invalid login"}}`, contentType: "application/json"})
	err := h.run("auth", "login", "--username", "bob", "--password", "pw")
	if err == nil || !strings.Contains(err.Error(), "サインインのパスワード") {
		t.Errorf("err = %v", err)
	}
}

// 生成コマンドの題名には、MT 7 以降（v4〜）の操作にだけ印が付く。
func TestShortMarksNewerOperations(t *testing.T) {
	s := spec.MustLoad()
	var marked, v2 int
	for _, c := range s.Commands {
		short := shortWithSince(c)
		since := commandSince(c)
		switch {
		case since >= markedSince:
			marked++
			if !strings.HasSuffix(short, "+]") {
				t.Errorf("%v: 印がありません: %s", c.Command, short)
			}
		case strings.HasSuffix(short, "+]"):
			t.Errorf("%v: v%d に印が付きました", c.Command, since)
		case since == 2:
			v2++
		}
	}
	if marked == 0 || v2 == 0 {
		t.Errorf("since が付いていません（marked=%d v2=%d）。make spec をやり直してください", marked, v2)
	}
}

// dump の種類は、すべて埋め込みの定義で引けること（引けないと版で止められない）。
// コンテンツタイプは v4 から、テンプレートは v2 から。
func TestDumpKindsResolveToSpecOperations(t *testing.T) {
	s := spec.MustLoad()
	kinds := append(append(append([]dumpKind{}, siteKinds...), systemKinds...), envKinds...)
	for _, k := range kinds {
		if s.Find(http.MethodGet, specPath(k.Path)) == nil {
			t.Errorf("%s (%s) が定義にありません", k.File, specPath(k.Path))
		}
	}
	if op := s.Find(http.MethodGet, specPath("/sites/%s/contentTypes")); op == nil || op.Since != 4 {
		t.Errorf("contentTypes: %+v", op)
	}
	if op := s.Find(http.MethodGet, specPath("/sites/12/templates")); op == nil || op.Since != 2 {
		t.Errorf("templates: %+v", op)
	}
}

func TestCheckAvailable(t *testing.T) {
	op := &spec.Operation{Method: "GET", Path: "/x", Since: 4}
	if err := checkAvailable(op, "v3"); err == nil {
		t.Error("v3 で v4 の操作を通しました")
	} else {
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("usageError ではありません: %T", err)
		}
	}
	for _, v := range []string{"v4", "v7", "v8"} {
		if err := checkAvailable(op, v); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	if err := checkAvailable(&spec.Operation{}, "v1"); err != nil {
		t.Errorf("since 0 を v1 で止めました: %v", err)
	}
}

// 版はプロファイルに紐づく。コマンドごとに変える口は無い。
func TestAPIVersionIsNotAGlobalFlag(t *testing.T) {
	h := newHarness(t)
	if err := h.run("--api-version", "v3", "sites", "entries", "list", "1"); err == nil {
		t.Fatal("--api-version がコマンドごとに効きました")
	}
	if len(h.doer.sent) != 0 {
		t.Errorf("送信されました: %v", h.doer.sent)
	}
}

func TestProfileSetAPIVersionPersists(t *testing.T) {
	h := newHarness(t)
	h.mustRun("profile", "set", "--api-version", "v3")
	f, err := h.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Profiles[f.Current].APIVersion; got != "v3" {
		t.Fatalf("APIVersion = %q", got)
	}
	h.reply(okJSON(`{"totalResults":0,"items":[]}`))
	h.mustRun("sites", "entries", "list", "1")
	if got := pathOf(t, h.last().URL); !strings.Contains(got, "/v3/") {
		t.Errorf("path = %s", got)
	}
}

func TestProfileSetRejectsInvalidVersion(t *testing.T) {
	h := newHarness(t)
	if err := h.run("profile", "set", "--api-version", "3"); err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("err = %v", err)
	}
	f, _ := h.store.Load()
	if got := f.Profiles[f.Current].APIVersion; got != "v7" {
		t.Errorf("壊れた値が保存されました: %q", got)
	}
}

// 壊れた版が入ってしまったプロファイルも、profile set で直せること。
func TestProfileSetCanRepairBrokenVersion(t *testing.T) {
	h := newHarness(t).withAPIVersion("latest")
	h.mustRun("profile", "set", "--api-version", "v3")
	h.reply(okJSON(`{"totalResults":0,"items":[]}`))
	h.mustRun("sites", "entries", "list", "1")
}

func TestProfileAddStoresAPIVersion(t *testing.T) {
	h := newHarness(t)
	h.mustRun("profile", "add", "mt6", "--base-url", "https://old.example.com/mt/mt-data-api.cgi", "--api-version", "v3")
	f, _ := h.store.Load()
	if got := f.Profiles["mt6"].APIVersion; got != "v3" {
		t.Errorf("APIVersion = %q", got)
	}
}

// プロファイルを使っているときの MTB_API_VERSION は効かせず、そう言う。
func TestEnvAPIVersionIsIgnoredWithProfile(t *testing.T) {
	h := newHarness(t, withEnvVars(map[string]string{"MTB_API_VERSION": "v3"}))
	h.reply(okJSON(`{"totalResults":0,"items":[]}`))
	h.mustRun("sites", "entries", "list", "1")
	if got := pathOf(t, h.last().URL); !strings.Contains(got, "/v7/") {
		t.Errorf("path = %s（プロファイルの版が使われていません）", got)
	}
	if !strings.Contains(h.errOut.String(), "MTB_API_VERSION") {
		t.Errorf("無視したことを言っていません: %q", h.errOut.String())
	}
}

// プロファイルを使わずに環境変数だけでつなぐときは MTB_API_VERSION が効く。
func TestEnvAPIVersionWithoutProfile(t *testing.T) {
	h := newHarness(t, withoutProfile(), withEnvVars(map[string]string{
		"MTB_BASE_URL":    "https://old.example.com/mt/mt-data-api.cgi",
		"MTB_API_VERSION": "v3",
	}))
	if err := h.run("sites", "content-types", "list", "1"); err == nil {
		t.Fatal("v3 にコンテンツタイプの一覧を送りました")
	}
	if len(h.doer.sent) != 0 {
		t.Errorf("送信されました: %v", h.doer.sent)
	}
}

// プロファイルを使っているときは、--base-url の末尾の /vN でも版は変わらない。
func TestBaseURLSuffixDoesNotOverrideProfileVersion(t *testing.T) {
	h := newHarness(t).withAPIVersion("v3")
	h.reply(okJSON(`{"totalResults":0,"items":[]}`))
	h.mustRun("--base-url", "https://example.com/mt/mt-data-api.cgi/v7", "sites", "entries", "list", "1")
	if got := pathOf(t, h.last().URL); got != "/mt/mt-data-api.cgi/v3/sites/1/entries" {
		t.Errorf("path = %s", got)
	}
}

func versionInvalid() stub {
	return stub{status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"API Version is invalid"}}`, contentType: "application/json"}
}

// 版を指定せずに足すと、v7 から順に /endpoints を叩き、通った版を既定にする。
func TestProfileAddDetectsNewestVersion(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.reply(versionInvalid(), versionInvalid(), versionInvalid(), versionInvalid(),
		okJSON(`{"totalResults":1,"items":[{"id":"list_endpoints"}]}`))

	h.mustRun("profile", "add", "mt6", "--base-url", "https://old.example.com/mt/mt-data-api.cgi")

	var paths []string
	for _, r := range h.doer.sent {
		paths = append(paths, pathOf(t, r.URL))
	}
	want := []string{"/v7/", "/v6/", "/v5/", "/v4/", "/v3/"}
	if len(paths) != len(want) {
		t.Fatalf("送った先 = %v", paths)
	}
	for i, w := range want {
		if !strings.Contains(paths[i], w) || !strings.HasSuffix(paths[i], "/endpoints") {
			t.Errorf("%d 回目 = %s, want %s…/endpoints", i+1, paths[i], w)
		}
		if h.doer.sent[i].Header.Get("X-MT-Authorization") != "" {
			t.Errorf("問い合わせに認証ヘッダが付いています")
		}
	}
	f, _ := h.store.Load()
	if got := f.Profiles["mt6"].APIVersion; got != "v3" {
		t.Errorf("APIVersion = %q, want v3", got)
	}
}

// 対話できれば、見つけた版を選んだ状態で一覧を出し、古い版にも下げられる。
// 見つけた版より新しいものは出さない（選んでも使えない）。
func TestProfileAddLetsUserPickVersion(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.prompt.NotInteractive = false
	h.prompt.Selects = []string{"v2"}
	h.prompt.Confirms = []bool{false}
	h.reply(versionInvalid(), versionInvalid(), versionInvalid(), versionInvalid(),
		okJSON(`{"items":[]}`))

	h.mustRun("profile", "add", "mt6", "--base-url", "https://old.example.com/mt/mt-data-api.cgi")

	f, _ := h.store.Load()
	if got := f.Profiles["mt6"].APIVersion; got != "v2" {
		t.Errorf("APIVersion = %q, want v2", got)
	}
	h2 := newHarness(t, withoutProfile())
	h2.prompt.NotInteractive = false
	h2.prompt.Selects = []string{"v4"}
	h2.reply(versionInvalid(), versionInvalid(), versionInvalid(), versionInvalid(),
		okJSON(`{"items":[]}`))
	if err := h2.run("profile", "add", "mt6", "--base-url", "https://old.example.com/mt/mt-data-api.cgi"); err == nil {
		t.Error("サーバに無い v4 を選べました")
	}
}

// 調べられなくても登録は止めない（v7 にして、直し方を伝える）。
// 版の問題でない失敗（ここでは 403）では、それ以上下の版を試さない。
func TestProfileAddFallsBackWhenDetectionFails(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.reply(stub{status: http.StatusForbidden, body: `{"error":{"code":403,"message":"Forbidden"}}`, contentType: "application/json"})

	h.mustRun("profile", "add", "prod", "--base-url", "https://example.com/mt/mt-data-api.cgi")

	if len(h.doer.sent) != 1 {
		t.Errorf("版の問題でない失敗のあとも問い合わせを続けました: %d 回", len(h.doer.sent))
	}
	f, _ := h.store.Load()
	if got := f.Profiles["prod"].APIVersion; got != "v7" {
		t.Errorf("APIVersion = %q", got)
	}
	if !strings.Contains(h.errOut.String(), "mtb profile set --api-version") {
		t.Errorf("直し方を伝えていません: %s", h.errOut.String())
	}
}

// Data API でない応答（サイトのトップを指した URL など）は、版として採らない。
func TestProfileAddRejectsNonDataAPIResponse(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.reply(stub{status: 200, body: "<html></html>", contentType: "text/html"})
	h.mustRun("profile", "add", "prod", "--base-url", "https://example.com/")
	if !strings.Contains(h.errOut.String(), "mt-data-api.cgi") {
		t.Errorf("URL の取り違えを伝えていません: %s", h.errOut.String())
	}
}

// 版を渡したら、問い合わせない（エージェントや CI で結果を決め打ちできる）。
func TestProfileAddWithVersionDoesNotProbe(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.mustRun("profile", "add", "mt6", "--base-url", "https://old.example.com/mt/mt-data-api.cgi", "--api-version", "v3")
	h.mustRun("profile", "add", "mt6b", "--base-url", "https://old.example.com/mt/mt-data-api.cgi/v2")
	if len(h.doer.sent) != 0 {
		t.Errorf("問い合わせました: %v", h.doer.sent)
	}
	f, _ := h.store.Load()
	if f.Profiles["mt6"].APIVersion != "v3" || f.Profiles["mt6b"].APIVersion != "v2" {
		t.Errorf("版 = %q, %q", f.Profiles["mt6"].APIVersion, f.Profiles["mt6b"].APIVersion)
	}
}

// MT 6 は新しすぎる版も断らず 200 を返す（6.2.2 で実測）。一覧の version の
// 最大値で版を決めること。version は文字列で来る。
func TestProfileAddDetectsMT6ByEndpointVersions(t *testing.T) {
	h := newHarness(t, withoutProfile())
	h.reply(okJSON(`{"totalResults":3,"items":[{"id":"a","version":"1"},{"id":"b","version":"3"},{"id":"c","version":"2"}]}`))

	h.mustRun("profile", "add", "mt6", "--base-url", "https://old.example.com/mt/mt-data-api.cgi")

	if len(h.doer.sent) != 1 {
		t.Errorf("問い合わせ回数 = %d", len(h.doer.sent))
	}
	f, _ := h.store.Load()
	if got := f.Profiles["mt6"].APIVersion; got != "v3" {
		t.Errorf("APIVersion = %q, want v3", got)
	}
	if !strings.Contains(h.errOut.String(), "MT 6.2") {
		t.Errorf("MT の版を添えていません: %s", h.errOut.String())
	}
}

// MT 6 は expiresIn を文字列で返す（remember は真偽値。6.2.2 で実測）。読めないとサインインそのものが失敗する。
func TestAuthLoginAcceptsStringExpiresIn(t *testing.T) {
	h := newHarness(t).withAPIVersion("v3")
	h.reply(okJSON(`{"accessToken":"t","sessionId":"s","expiresIn":"3600","remember":true}`))
	h.mustRun("auth", "login", "--username", "bob", "--password", "pw")
	cred, err := h.store.Credential("prod")
	if err != nil {
		t.Fatal(err)
	}
	if cred.ExpiresAt != testNow+3600 {
		t.Errorf("ExpiresAt = %d", cred.ExpiresAt)
	}
}
