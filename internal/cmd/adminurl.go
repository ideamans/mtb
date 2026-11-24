package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ideamans/mtb/internal/config"
	"github.com/ideamans/mtb/internal/i18n"
)

// adminKind は mtb admin-url が案内できる画面の種類1つです。
//
// URL の形は MT の管理画面そのもの（lib/MT/App/CMS.pm の object_edit_uri と
// 一覧の __mode=list）に合わせてある。編集画面は __mode=view&_type=<型>&id=<ID>、
// 一覧は __mode=list&_type=<型>。サイトに属するものは blog_id を付ける。
type adminKind struct {
	// name はコマンドの引数（mtb のコマンドと同じ複数形）、aliases は単数形など
	name    string
	aliases []string
	// typ は MT の _type。空なら mode だけで決まる画面
	typ string
	// listMode は ID を省いたときの __mode（既定は list）。editMode は ID を付けたとき（既定は view）
	listMode string
	editMode string
	// site はサイトの ID が要るか。system は常にシステム（blog_id=0）か
	site   bool
	system bool
	// noID は ID を取らない画面（ダッシュボード・設定）
	noID bool
	// minAPI はこの画面がある最初の Data API の版（コンテンツタイプは MT 7 = v4 から）
	minAPI int
	// label は --help に出す説明（言語はヘルプを組むときに決まるので関数にする）
	label func() string
}

var adminKinds = []adminKind{
	{name: "dashboard", noID: true, listMode: "dashboard", label: func() string {
		return i18n.T("ダッシュボード（サイトを省くとシステム）", "dashboard (the system one without a site)")
	}},
	{name: "settings", noID: true, site: true, listMode: "cfg_prefs", label: func() string { return i18n.T("サイトの設定（全般）", "site settings (general)") }},
	{name: "sites", aliases: []string{"site"}, label: func() string {
		return i18n.T("サイト（ID 付きならそのサイトのダッシュボード）", "sites (with an ID, that site's dashboard)")
	}},
	{name: "entries", aliases: []string{"entry"}, typ: "entry", site: true, label: func() string { return i18n.T("記事", "entries") }},
	{name: "pages", aliases: []string{"page"}, typ: "page", site: true, label: func() string { return i18n.T("ウェブページ", "pages") }},
	{name: "categories", aliases: []string{"category"}, typ: "category", site: true, label: func() string { return i18n.T("カテゴリ", "categories") }},
	{name: "folders", aliases: []string{"folder"}, typ: "folder", site: true, label: func() string { return i18n.T("フォルダ", "folders") }},
	{name: "assets", aliases: []string{"asset"}, typ: "asset", site: true, label: func() string { return i18n.T("アセット", "assets") }},
	{name: "templates", aliases: []string{"template"}, typ: "template", site: true, listMode: "list_template", label: func() string { return i18n.T("テンプレート", "templates") }},
	{name: "content-types", aliases: []string{"content-type"}, typ: "content_type", site: true, minAPI: 4, label: func() string { return i18n.T("コンテンツタイプ", "content types") }},
	{name: "content-data", typ: "content_data", site: true, minAPI: 4, label: func() string {
		return i18n.T("コンテンツデータ（--content-type-id が要る）", "content data (needs --content-type-id)")
	}},
	{name: "users", aliases: []string{"user", "authors", "author"}, typ: "author", system: true, label: func() string { return i18n.T("ユーザー", "users") }},
	{name: "roles", aliases: []string{"role"}, typ: "role", system: true, label: func() string { return i18n.T("ロール", "roles") }},
	{name: "logs", aliases: []string{"log"}, typ: "log", noID: true, label: func() string {
		return i18n.T("ログ（サイトを省くとシステム）", "activity log (the system one without a site)")
	}},
}

func findAdminKind(name string) (adminKind, bool) {
	for _, k := range adminKinds {
		if k.name == name {
			return k, true
		}
		for _, a := range k.aliases {
			if a == name {
				return k, true
			}
		}
	}
	return adminKind{}, false
}

// adminURLRequest は1つの画面を組み立てるのに要るものです。
type adminURLRequest struct {
	kind          adminKind
	id            string
	siteID        string
	contentTypeID string
}

// buildAdminURL は管理画面のスクリプトの URL（…/mt.cgi）に問い合わせを付けます。
func buildAdminURL(admin string, req adminURLRequest) (string, error) {
	k := req.kind
	// URL に入るので、ID は数字だけ受ける（エラーの文面やうっかりの値が URL になって「開けない」で気づくのでは遅い）
	for _, v := range []struct{ name, value string }{{"ID", req.id}, {"--site-id", req.siteID}, {"--content-type-id", req.contentTypeID}} {
		if v.value != "" && !isDigitsOnly(v.value) {
			return "", usagef(i18n.T("%s は数字で指定してください: %q", "%s must be a number: %q"), v.name, v.value)
		}
	}
	if req.id != "" && k.noID {
		return "", usagef(i18n.T("%s は ID を取りません", "%s takes no ID"), k.name)
	}
	q := url.Values{}
	switch {
	case k.name == "sites":
		if req.id == "" {
			q.Set("__mode", "list")
			q.Set("_type", "website")
			q.Set("blog_id", "0")
		} else {
			q.Set("__mode", "dashboard")
			q.Set("blog_id", req.id)
		}
	case k.name == "dashboard" || k.name == "logs":
		if k.name == "dashboard" {
			q.Set("__mode", "dashboard")
		} else {
			q.Set("__mode", "list")
			q.Set("_type", k.typ)
		}
		q.Set("blog_id", firstNonEmptyString(req.siteID, "0"))
	default:
		if k.site && req.siteID == "" {
			return "", usagef(i18n.T(
				"%s はサイトに属します。--site-id か、プロファイルの既定のサイト（mtb profile set --site-id）を指定してください",
				"%s belong to a site; pass --site-id or set the profile's default site (mtb profile set --site-id)"), k.name)
		}
		mode := firstNonEmptyString(k.listMode, "list")
		if req.id != "" {
			mode = firstNonEmptyString(k.editMode, "view")
		}
		q.Set("__mode", mode)
		if k.typ != "" && !(req.id == "" && k.listMode != "" && k.listMode != "list") {
			q.Set("_type", k.typ)
		}
		if k.typ == "content_data" {
			if req.contentTypeID == "" {
				return "", usagef("%s", i18n.T(
					"コンテンツデータの画面には --content-type-id が要ります",
					"content data needs --content-type-id"))
			}
			q.Set("type", "content_data_"+req.contentTypeID)
			if req.id != "" {
				q.Set("content_type_id", req.contentTypeID)
			}
		}
		switch {
		case k.system && req.id == "":
			q.Set("blog_id", "0")
		case k.site:
			q.Set("blog_id", req.siteID)
		}
		if req.id != "" {
			q.Set("id", req.id)
		}
	}
	return admin + "?" + q.Encode(), nil
}

func (a *App) newAdminURLCmd() *cobra.Command {
	var siteID, contentTypeID string
	var names []string
	width := 0
	for _, k := range adminKinds {
		n := strings.Join(append([]string{k.name}, k.aliases...), ", ")
		names = append(names, n)
		width = max(width, len(n))
	}
	var lines []string
	for i, k := range adminKinds {
		lines = append(lines, fmt.Sprintf("  %-*s  %s", width, names[i], k.label()))
	}
	kinds := strings.Join(lines, "\n")
	cmd := &cobra.Command{
		Use:     i18n.T("admin-url <種類> [ID]", "admin-url <kind> [ID]"),
		Aliases: []string{"management-url"},
		Short:   i18n.T("オブジェクトの管理画面の URL を出す", "Print the admin screen URL of an object"),
		Long: i18n.T(
			"MT の管理画面で、そのオブジェクトを開く URL を出します。ID を付ければ編集画面、\n"+
				"省けば一覧です。通信はしません（URL を組み立てるだけ）。\n\n"+
				"mtb で記事やテンプレートを書き換えたら、この URL を添えて「詳しくは管理画面で」と\n"+
				"案内すると、利用者が結果をすぐ確かめられます。\n\n"+
				"管理画面の URL はプロファイルの --admin-url（mtb profile set --admin-url）か\n"+
				"MTB_ADMIN_URL。どちらも無ければ Data API の URL から推測し（mt-data-api.cgi を\n"+
				"mt.cgi に替える）、推測したことを標準エラーに出します。サイトは --site-id か\n"+
				"プロファイルの既定のサイト。\n\n"+
				"種類:\n"+kinds+"\n\n"+
				"例:\n"+
				"  mtb admin-url entries 123 --site-id 1\n"+
				"  mtb admin-url templates --site-id 1\n"+
				"  mtb admin-url content-data 45 --site-id 1 --content-type-id 3",
			"Print the URL that opens the object in the MT admin screen: with an ID the edit\n"+
				"screen, without one the list. Nothing is sent (the URL is only put together).\n\n"+
				"After changing entries or templates with mtb, give this URL (\"see the admin\n"+
				"screen for details\") so the user can check the result straight away.\n\n"+
				"The admin screen URL is the profile's --admin-url (mtb profile set --admin-url)\n"+
				"or MTB_ADMIN_URL. Without either it is guessed from the Data API URL\n"+
				"(mt-data-api.cgi becomes mt.cgi) and standard error says so. The site is\n"+
				"--site-id or the profile's default site.\n\n"+
				"Kinds:\n"+kinds+"\n\n"+
				"Examples:\n"+
				"  mtb admin-url entries 123 --site-id 1\n"+
				"  mtb admin-url templates --site-id 1\n"+
				"  mtb admin-url content-data 45 --site-id 1 --content-type-id 3"),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, ok := findAdminKind(args[0])
			if !ok {
				names := make([]string, 0, len(adminKinds))
				for _, k := range adminKinds {
					names = append(names, k.name)
				}
				return usagef(i18n.T("種類 %q はありません（%s）", "unknown kind %q (%s)"), args[0], strings.Join(names, ", "))
			}
			r, err := a.resolve()
			if err != nil {
				return err
			}
			if k.minAPI > 0 && config.VersionNumber(r.APIVersion) < k.minAPI {
				return usagef(i18n.T(
					"%s は MT 7 以降の画面です（このプロファイルは Data API %s）",
					"%s exist from MT 7 on (this profile speaks Data API %s)"), k.name, config.DescribeVersion(r.APIVersion))
			}
			admin := r.AdminURL
			if admin == "" {
				if admin = config.GuessAdminURL(r.BaseURL); admin == "" {
					return errors.New(i18n.T(
						"管理画面の URL が分かりません。mtb profile set --admin-url https://example.com/cgi-bin/mt/mt.cgi で登録してください",
						"the admin screen URL is unknown; register it with: mtb profile set --admin-url https://example.com/cgi-bin/mt/mt.cgi"))
				}
				fmt.Fprintf(a.env.Err, i18n.T(
					"注意: 管理画面の URL は Data API の URL から推測しました（%s）。違っていたら mtb profile set --admin-url で登録してください\n",
					"note: the admin screen URL was guessed from the Data API URL (%s); if it is wrong, register it with mtb profile set --admin-url\n"), admin)
			}
			req := adminURLRequest{kind: k, siteID: firstNonEmptyString(siteID, r.SiteID), contentTypeID: contentTypeID}
			if len(args) == 2 {
				req.id = args[1]
			}
			if k.system {
				req.siteID = ""
			}
			u, err := buildAdminURL(admin, req)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.env.Out, u)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&siteID, "site-id", "", i18n.T("サイトの ID（省くとプロファイルの既定のサイト）", "site ID (defaults to the profile's default site)"))
	f.StringVar(&contentTypeID, "content-type-id", "", i18n.T("コンテンツタイプの ID（content-data に要る）", "content type ID (needed for content-data)"))
	return cmd
}

func isDigitsOnly(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
