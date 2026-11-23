package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// RunLogger は動作ログ（mtb logs）の書き手です。書き先と実行 ID は呼び出し側（cmd）が持ちます。
type RunLogger interface {
	// Log は出来事1つを残します。level は debug|info|warn|error。
	Log(level, event, msg string, fields map[string]any)
	// Debug が真なら、送受信の詳細（伏せたヘッダと切り詰めた本文）も残します。
	Debug() bool
}

// DebugBodyLimit は debug のときに残す本文の長さの上限です。
const DebugBodyLimit = 2000

func (c *Client) logf(level, event, msg string, fields map[string]any) {
	if c.Log != nil {
		c.Log.Log(level, event, msg, fields)
	}
}

func (c *Client) versionedPath(p string) string {
	return "/" + c.APIVersion + "/" + strings.TrimPrefix(p, "/")
}

// logAttempt は送信1回を動作ログに残します。成功は info、失敗は warn
// （コマンドとして失敗したかは終了時の end が error で残す）。
func (c *Client) logAttempt(req *http.Request, info auditInfo, started time.Time, resp *Response, err error) {
	if c.Log == nil || req == nil {
		return
	}
	f := map[string]any{
		"method": info.method,
		"path":   c.versionedPath(info.path),
		"host":   req.URL.Host,
		"ms":     max(c.now().Sub(started).Milliseconds(), 0),
		"write":  info.write,
	}
	level, msg := "info", ""
	status := 0
	if resp != nil {
		status = resp.Status
		f["status"] = status
	}
	if class := ErrorClass(err, status); class != "" {
		f["class"] = class
		level = "warn"
		if err != nil {
			msg = truncateAudit(err.Error(), AuditValueLimit)
		} else {
			msg = errorMessage(resp)
		}
	}
	if c.Log.Debug() {
		h := map[string]string{}
		for k, vs := range req.Header {
			v := strings.Join(vs, ", ")
			if IsSecretName(k) || strings.EqualFold(k, "Cookie") || strings.EqualFold(k, authHeader) {
				v = "[redacted]"
			}
			h[k] = v
		}
		f["headers"] = h
		if len(info.query) > 0 {
			f["query"] = auditValues(info.query)
		}
		if len(info.form) > 0 {
			f["params"] = auditValues(info.form)
		}
		if resp != nil {
			f["body"] = RedactValue(string(resp.Body), DebugBodyLimit)
		}
	}
	c.logf(level, "http", msg, f)
}

// ErrorClass は失敗の種類を返します。成功なら空。
//
//	dns      名前を引けない
//	tls      証明書・TLS の握手
//	timeout  時間切れ（接続でも応答待ちでも）
//	tcp      つながらない（拒否・到達不能・切断）
//	http     応答はあったが 400 以上
//	other    それ以外（応答を読めないなど）
func ErrorClass(err error, status int) string {
	if err == nil {
		if status >= 400 {
			return "http"
		}
		return ""
	}
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var recErr tls.RecordHeaderError
	var netErr net.Error
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr),
		errors.As(err, &invalid), errors.As(err, &recErr), strings.Contains(err.Error(), "tls:"):
		return "tls"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.As(err, &opErr):
		return "tcp"
	}
	return "other"
}
