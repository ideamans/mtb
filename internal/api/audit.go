package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// AuditRecord は監査ログの1行です（mtb のローカルの監査ログ）。
//
// 項目は MtbPro（サーバ側の監査ログ）に合わせてあります。MtbPro は MT に届いた
// 全員の操作を残し、こちらは「この mtb が送った操作」だけを手元に残します。
// 同じものを別の場所で見ている、と感じられるよう、伏せ方と切り詰め方も揃えます。
//
// JSON のキーは外から読まれる約束です（mtb studio が読む）。変えないこと。
type AuditRecord struct {
	TS         string         `json:"ts"`
	Profile    string         `json:"profile"`
	Host       string         `json:"host"`
	APIVersion string         `json:"apiVersion"`
	Method     string         `json:"method"`
	Path       string         `json:"path"`
	Query      map[string]any `json:"query"`
	Params     map[string]any `json:"params"`
	Status     int            `json:"status"`
	MS         int64          `json:"ms"`
	Note       string         `json:"note"`
	User       string         `json:"user"`
	Error      string         `json:"error,omitempty"`
	Write      bool           `json:"write"`
	// Run は実行1回ごとの ID です。動作ログ（mtb logs）の同じ ID の行と対応します。
	Run string `json:"run"`
}

// Auditor は監査ログの書き手です。プロファイル名や書き込み先を知っているのは
// 呼び出し側（cmd）なので、api は1行分を組み立てて渡すだけにします。
type Auditor interface {
	Record(r AuditRecord)
}

// AuditValueLimit は1つの値を残す長さの上限です（MtbPro の既定と同じ）。
const AuditValueLimit = 2000

// auditSecret は値を伏せるパラメータ名です。JSON の値の中のキーにも効かせます。
var auditSecret = regexp.MustCompile(`(?i)password|passwd|secret|token|session|authorization|api[_-]?key|credential`)

// auditNoise は監査ログに入れても意味のない、Data API の制御用パラメータです。
var auditNoise = map[string]bool{"__method": true, "suppressResponseCodes": true}

// IsSecretName は値を伏せるべき名前（パラメータ・フラグ・ヘッダ）かどうかです。
func IsSecretName(name string) bool { return auditSecret.MatchString(name) }

// RedactValue は値を残せる形にします。JSON の中の秘密を伏せ、limit 文字を超えれば
// 切り詰めて元の長さと SHA-256 を添えます。
func RedactValue(s string, limit int) string { return truncateAudit(redactJSON(s), limit) }

// RedactValues はパラメータを残せる形にします（監査ログと同じ伏せ方）。
func RedactValues(v url.Values) map[string]any { return auditValues(v) }

// auditInfo は送信1回分の、監査ログに要る情報です。roundTrip に渡します。
type auditInfo struct {
	method string
	path   string
	query  url.Values
	form   url.Values
	upload *Upload
	// write は「何かを変える呼び出し」かどうかです。トークンの取り直しは
	// POST でも何も変えないので読み取りと同じ扱いにします。
	write bool
}

func newAuditInfo(method, path string, query, form url.Values, upload *Upload) auditInfo {
	m := strings.ToUpper(method)
	return auditInfo{
		method: m, path: path, query: query, form: form, upload: upload,
		write: m != "GET" && m != "HEAD" && path != "/token",
	}
}

// buildRecord は送信の前後の情報から1行を作ります。
func (c *Client) buildRecord(info auditInfo, started time.Time, resp *Response, err error) AuditRecord {
	r := AuditRecord{
		TS:         started.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		APIVersion: c.APIVersion,
		Method:     info.method,
		Path:       "/" + c.APIVersion + "/" + strings.TrimPrefix(info.path, "/"),
		Query:      auditValues(info.query),
		Params:     auditValues(info.form),
		Note:       c.AuditNote,
		Write:      info.write,
	}
	if u, perr := url.Parse(c.BaseURL); perr == nil {
		r.Host = u.Host
	}
	if info.upload != nil {
		sum := sha256.Sum256(info.upload.Content)
		r.Params[info.upload.Field] = map[string]any{
			"fileName": info.upload.Filename,
			"bytes":    len(info.upload.Content),
			"sha256":   hex.EncodeToString(sum[:]),
		}
	}
	r.MS = c.now().Sub(started).Milliseconds()
	if r.MS < 0 {
		r.MS = 0
	}
	switch {
	case err != nil:
		r.Error = truncateAudit(err.Error(), AuditValueLimit)
	case resp != nil:
		r.Status = resp.Status
		if resp.Status >= 400 {
			r.Error = errorMessage(resp)
		}
	}
	return r
}

// audit は Auditor があれば1行を渡します。
func (c *Client) audit(info auditInfo, started time.Time, resp *Response, err error) {
	if c.Auditor == nil {
		return
	}
	c.Auditor.Record(c.buildRecord(info, started, resp, err))
}

// auditValues はパラメータを残せる形にします。秘密は伏せ、長い値は切り詰め、
// 1つだけの値は配列にしません（MtbPro と同じ形）。
func auditValues(v url.Values) map[string]any {
	out := map[string]any{}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if auditNoise[k] {
			continue
		}
		vals := make([]any, 0, len(v[k]))
		for _, s := range v[k] {
			if auditSecret.MatchString(k) {
				vals = append(vals, "[redacted]")
				continue
			}
			vals = append(vals, truncateAudit(redactJSON(s), AuditValueLimit))
		}
		if len(vals) == 1 {
			out[k] = vals[0]
		} else {
			out[k] = vals
		}
	}
	return out
}

// redactJSON は JSON として読める値の中のキーで秘密を伏せ、書き直します。
// 読めない値・伏せるものが無い値はそのまま返します（原文の並びを崩さないため）。
func redactJSON(s string) string {
	t := strings.TrimSpace(s)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return s
	}
	var doc any
	if err := json.Unmarshal([]byte(t), &doc); err != nil {
		return s
	}
	if !walkRedact(doc) {
		return s
	}
	buf, err := json.Marshal(doc)
	if err != nil {
		return s
	}
	return string(buf)
}

func walkRedact(d any) bool {
	hit := false
	switch x := d.(type) {
	case map[string]any:
		for k, v := range x {
			if _, isObj := v.(map[string]any); !isObj {
				if _, isArr := v.([]any); !isArr && auditSecret.MatchString(k) && v != nil {
					x[k] = "[redacted]"
					hit = true
					continue
				}
			}
			if walkRedact(v) {
				hit = true
			}
		}
	case []any:
		for _, v := range x {
			if walkRedact(v) {
				hit = true
			}
		}
	}
	return hit
}

// truncateAudit は長い値を切り詰め、元の長さと SHA-256 を添えます（MtbPro と同じ書式）。
// 長さは文字数で数えます。
func truncateAudit(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	r := []rune(s)
	return fmt.Sprintf("%s... [truncated: %d chars, sha256=%s]", string(r[:limit]), len(r), hex.EncodeToString(sum[:]))
}

// errorMessage は失敗した応答から短い説明を取り出します。
func errorMessage(resp *Response) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if resp.IsJSON() && json.Unmarshal(resp.Body, &e) == nil && e.Error.Message != "" {
		return truncateAudit(e.Error.Message, AuditValueLimit)
	}
	return fmt.Sprintf("HTTP %d", resp.Status)
}
