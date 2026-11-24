package config

import (
	"net/url"
	"strings"
)

// DataAPIScript と AdminScript は MT の既定のスクリプト名です（mt-config.cgi の
// DataAPIScript・AdminScript）。推測にだけ使います。
const (
	DataAPIScript = "mt-data-api.cgi"
	AdminScript   = "mt.cgi"
)

// NormalizeAdminURL は管理画面の URL から、問い合わせ（?__mode=…）と
// 断片（#…）と前後の空白を落とします。ブラウザのアドレス欄からそのまま
// 貼っても、スクリプトまでの URL になるようにするためです。
func NormalizeAdminURL(admin string) string {
	admin = strings.TrimSpace(admin)
	if i := strings.IndexAny(admin, "?#"); i >= 0 {
		admin = admin[:i]
	}
	return admin
}

// GuessDataAPIURL は管理画面の URL から Data API の URL を推測します。
//
// Data API のスクリプトは、管理画面と同じディレクトリ（CGIPath）に置かれる
// のが普通です。末尾の1区切り（mt.cgi、MT 8 以降の PSGI なら admin など）を
// mt-data-api.cgi に替え、ディレクトリで終わっていれば足します。
// 形が読めなければ空文字を返します（既定値を出さないだけで、止めはしない）。
func GuessDataAPIURL(admin string) string {
	admin = NormalizeAdminURL(admin)
	u, err := url.Parse(admin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	dir := u.Path
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i+1]
	} else {
		dir = "/"
	}
	u.Path = dir + DataAPIScript
	return u.String()
}

// GuessAdminURL は Data API の URL から管理画面の URL を推測します。
// 末尾が mt-data-api.cgi のときだけ、同じディレクトリの mt.cgi にします。
// 推測できなければ空文字です。
func GuessAdminURL(base string) string {
	base, _ = NormalizeBaseURL(base, "")
	i := strings.LastIndex(base, "/")
	if i < 0 || base[i+1:] != DataAPIScript {
		return ""
	}
	return base[:i+1] + AdminScript
}

// IsDataAPIURL は URL の末尾が mt-data-api.cgi（版の /vN 付きも）かです。
// 管理画面の URL を聞いたのに Data API の URL が入ったことに気づくために使います。
func IsDataAPIURL(s string) bool {
	return GuessAdminURL(s) != ""
}
