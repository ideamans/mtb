// Package config は接続先プロファイルと、そのプロファイルで取得した認証情報を
// 扱います。
//
// 外部依存（ファイルシステムと環境変数）は Storage と Environ の2つの
// インタフェースに閉じ込めてあります。テストは MemStorage と MapEnv を渡す
// ことで、実ファイルにもプロセス環境にも触れずに済みます。
//
// 設定と認証情報はファイルを分けています。config.json は接続先の住所録で
// あって人に見せられますが、credentials.json はセッションとアクセストークン
// そのものなので 0600 で保存し、共有やバックアップの対象から外せるように
// しておきたいからです。
package config

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ideamans/mtb/internal/i18n"
)

const (
	// DefaultAPIVersion は Data API のエンドポイントバージョンです。
	DefaultAPIVersion = "v7"
	// DefaultClientID は認証時に名乗るクライアントIDです。MT 側はこれを
	// セッションの区別に使うので、CLI では固定してよいものです。
	DefaultClientID = "mtb"

	// ConfigFile と CredentialsFile は設定ディレクトリ内のファイル名です。
	ConfigFile      = "config.json"
	CredentialsFile = "credentials.json"

	// expirySkew はアクセストークンを「もう切れたもの」として扱う前倒し幅です。
	// 送信中に失効して 401 を踏むのを避けます。
	expirySkew = 60 * time.Second
)

// Profile は接続先1つ分の設定です。
type Profile struct {
	// BaseURL は mt-data-api.cgi までの URL です（末尾の /vN は含めません）。
	BaseURL string `json:"baseUrl"`
	// APIVersion は空なら v7 です。
	APIVersion string `json:"apiVersion,omitempty"`
	// ClientID は空なら mtb です。
	ClientID string `json:"clientId,omitempty"`
	// Username は `mtb auth login` の入力を省くための控えです。
	// パスワードは保存しません。
	Username string `json:"username,omitempty"`
	// DefaultSiteID があると --site-id を省けます。
	DefaultSiteID string `json:"defaultSiteId,omitempty"`
	// InsecureSkipVerify は自己署名証明書の検証用サーバ向けの逃げ道です。
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// Credential は1プロファイル分の認証情報です。
type Credential struct {
	// SessionID はアクセストークンを取り直すための種です。これがあるかぎり
	// パスワードの再入力なしにトークンを更新できます。
	SessionID string `json:"sessionId,omitempty"`
	// AccessToken は各リクエストに付ける短命なトークンです。
	AccessToken string `json:"accessToken,omitempty"`
	// ExpiresAt はアクセストークンの失効時刻（Unix秒）です。
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	Remember  bool   `json:"remember,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Expired は now の時点でアクセストークンが使えないかを返します。
func (c *Credential) Expired(now time.Time) bool {
	if c == nil || c.AccessToken == "" {
		return true
	}
	if c.ExpiresAt == 0 {
		return false
	}
	return now.Add(expirySkew).After(time.Unix(c.ExpiresAt, 0))
}

// ExpiresIn は now から失効までの残り時間です。失効済みなら 0 です。
func (c *Credential) ExpiresIn(now time.Time) time.Duration {
	if c == nil || c.ExpiresAt == 0 {
		return 0
	}
	d := time.Unix(c.ExpiresAt, 0).Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// File は config.json の中身です。
type File struct {
	// Current は --profile も MTB_PROFILE も無いときに使うプロファイル名です。
	Current  string              `json:"current,omitempty"`
	Profiles map[string]*Profile `json:"profiles"`
}

// Names はプロファイル名を名前順で返します。
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Profiles))
	for name := range f.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolved は環境変数とフラグを重ねた「実際に使う接続先」です。
type Resolved struct {
	// Name はプロファイル名です。設定ファイルを使っていないときは "(env)" です。
	Name       string
	BaseURL    string
	APIVersion string
	ClientID   string
	Username   string
	SiteID     string
	Insecure   bool
	// IgnoredAPIVersion は、プロファイルを使っているために効かせなかった
	// MTB_API_VERSION の値です。黙って無視すると、効いていると思い込んだまま
	// になるので、呼び出し側が警告に使います。
	IgnoredAPIVersion string
	// FromFile は設定ファイル由来のプロファイルが見つかったかどうかです。
	// 認証情報をどのキーで保存するかの判断に使います。
	FromFile bool
}

// Overrides はコマンドラインからの上書きです。空文字は「指定なし」を意味します。
//
// Data API の版は上書きできません。版はサーバ（＝プロファイル）で決まるもので、
// コマンドごとに変えると「v3 の MT に v7 で送る」取り違えが起きるためです。
// 変えるなら mtb profile set --api-version です。
type Overrides struct {
	Profile  string
	BaseURL  string
	ClientID string
	SiteID   string
}

// ErrNoProfile はどこからも接続先を決められなかったときのエラーです。
var ErrNoProfile = i18n.Error("接続先が未設定です", "no connection target is configured")

// ErrNoSuchProfile は名指しされたプロファイルが設定に無いときのエラーです。
var ErrNoSuchProfile = i18n.Error("プロファイルがありません", "no such profile")

// NormalizeBaseURL は末尾のスラッシュと、うっかり付けた /v7 を取り除きます。
// リファレンスに載っている URL をそのまま貼っても通るようにするためのものです。
func NormalizeBaseURL(base, version string) (string, string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if i := strings.LastIndex(base, "/"); i >= 0 {
		if last := base[i+1:]; strings.HasPrefix(last, "v") && isDigits(last[1:]) {
			return base[:i], last
		}
	}
	if version == "" {
		version = DefaultAPIVersion
	}
	return base, version
}

// VersionNumber は "v3" を 3 にします。形が違えば 0 です。
func VersionNumber(version string) int {
	if !strings.HasPrefix(version, "v") || !isDigits(version[1:]) {
		return 0
	}
	n := 0
	for _, r := range version[1:] {
		n = n*10 + int(r-'0')
		if n > 1000 {
			return 0
		}
	}
	return n
}

// MTReleases は、Data API の各版を最新として持つ MT の版です。
//
// 配布物の lib/MT/App/DataAPI.pm の API_VERSION（6.0.8・6.1.3・6.2.2・6.8.8・7.0・
// 7 r.4701・7 r.5510・8.0.2・8.4.0・8.8.0・9.0.9・9.3.1）と、v5・v6 を新設した
// リリースノート（7 r.5401・7 r.5404）で確かめた（2026-11-20）。版を選ばせるときに
// 「v3」だけでは分からないので、MT の版を添えるのに使います。
var MTReleases = map[int]string{
	1: "MT 6.0",
	2: "MT 6.1",
	3: "MT 6.2–6.8",
	4: "MT 7.0–7 r.5304",
	5: "MT 7 r.5401–r.5403",
	6: "MT 7 r.5404+ / MT 8.0–8.4",
	7: "MT 8.8+ / MT 9",
}

// DescribeVersion は "v3（MT 6.2–6.8）" の形にします。対応表に無い版はそのまま返します。
func DescribeVersion(version string) string {
	if mt, ok := MTReleases[VersionNumber(version)]; ok {
		return fmt.Sprintf(i18n.T("%s（%s）", "%s (%s)"), version, mt)
	}
	return version
}

// UsesSignInPassword は、その版の /authentication がサインインのパスワードを
// 受け取るかです。v1・v2 はサインインのパスワード、v3 から（MT 6.2〜）は
// Webサービスパスワードを受け取ります。
func UsesSignInPassword(version string) bool {
	n := VersionNumber(version)
	return n == 1 || n == 2
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
