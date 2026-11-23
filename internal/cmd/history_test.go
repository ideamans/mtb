package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ideamans/mtb/internal/api"
)

// testAuditFile は固定時刻（2023-11）での既定の書き先です。
const testAuditFile = "mem:audit/2023-11.jsonl"

func auditLines(t *testing.T, h *harness, name string) []api.AuditRecord {
	t.Helper()
	var out []api.AuditRecord
	for _, line := range strings.Split(strings.TrimSpace(h.files[name]), "\n") {
		if line == "" {
			continue
		}
		var r api.AuditRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("1行が JSON ではありません: %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

func TestWritesAreAuditedLocally(t *testing.T) {
	h := newHarness(t, withEnvVars(map[string]string{"MTB_AUDIT_NOTE": "春の改修"})).
		reply(okJSON(`{"id":5}`))
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{"title":"新しい記事"}`)

	recs := auditLines(t, h, testAuditFile)
	if len(recs) != 1 {
		t.Fatalf("記録 = %d 行、1行のはず: %q", len(recs), h.files[testAuditFile])
	}
	r := recs[0]
	if r.Method != "POST" || r.Path != "/v7/sites/1/entries" || r.Status != 200 || !r.Write {
		t.Errorf("記録が違います: %+v", r)
	}
	if r.Profile != "prod" || r.Host != "example.com" || r.APIVersion != "v7" || r.Note != "春の改修" || r.User != "alice" {
		t.Errorf("付帯情報が違います: %+v", r)
	}
	if !strings.Contains(r.Params["entry"].(string), "新しい記事") {
		t.Errorf("params = %v", r.Params)
	}
	if r.TS != "2023-11-14T22:13:20.000Z" {
		t.Errorf("ts = %q（UTC のはず）", r.TS)
	}
}

func TestReadsAreAuditedOnlyWhenAsked(t *testing.T) {
	h := newHarness(t).reply(okJSON(`{}`))
	h.mustRun("sites", "entries", "list", "--site-id", "1")
	if _, ok := h.files[testAuditFile]; ok {
		t.Fatal("既定では読み取りを残さないはず")
	}

	h = newHarness(t, withEnvVars(map[string]string{"MTB_AUDIT_READS": "1"})).reply(okJSON(`{}`))
	h.mustRun("sites", "entries", "list", "--site-id", "1", "--limit", "3")
	recs := auditLines(t, h, testAuditFile)
	if len(recs) != 1 || recs[0].Method != "GET" || recs[0].Write || recs[0].Query["limit"] != "3" {
		t.Errorf("読み取りの記録が違います: %+v", recs)
	}
}

func TestAuditLogRedactsSecrets(t *testing.T) {
	h := newHarness(t, withEnvVars(map[string]string{"MTB_PASSWORD": "pw-should-not-appear"}))
	h.reply(okJSON(`{"accessToken":"a","sessionId":"s","expiresIn":3600}`))
	h.mustRun("auth", "login", "--username", "alice")

	raw := h.files[testAuditFile]
	if strings.Contains(raw, "pw-should-not-appear") {
		t.Fatalf("パスワードが監査ログに残っています: %s", raw)
	}
	if !strings.Contains(raw, `"password":"[redacted]"`) {
		t.Errorf("伏せた印がありません: %s", raw)
	}

	// JSON の値の中のキーも伏せる
	h = newHarness(t).reply(okJSON(`{}`))
	h.mustRun("users", "create", "--user", `{"name":"bob","password":"inner-secret"}`)
	if raw := h.files[testAuditFile]; strings.Contains(raw, "inner-secret") || !strings.Contains(raw, "bob") {
		t.Errorf("JSON の中の秘密が伏せられていません: %s", raw)
	}
}

func TestAuditLogTruncatesLongValues(t *testing.T) {
	long := strings.Repeat("あ", api.AuditValueLimit+10)
	h := newHarness(t).reply(okJSON(`{}`))
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{"body":"`+long+`"}`)
	r := auditLines(t, h, testAuditFile)[0]
	v := r.Params["entry"].(string)
	if !strings.Contains(v, "[truncated: ") || !strings.Contains(v, "sha256=") {
		t.Errorf("切り詰められていません（%d 文字）", len([]rune(v)))
	}
}

func TestAuditLogFailuresAreRecordedAndDoNotBreakCommands(t *testing.T) {
	h := newHarness(t).reply(stub{status: 403, body: `{"error":{"code":403,"message":"Permission denied."}}`, contentType: "application/json"})
	_ = h.run("sites", "entries", "delete", "--site-id", "1", "--entry-id", "9", "--yes")
	r := auditLines(t, h, testAuditFile)[0]
	if r.Status != 403 || r.Error != "Permission denied." || r.Method != "DELETE" {
		t.Errorf("失敗の記録が違います: %+v", r)
	}

	// 書けなくても止めない（警告だけ）
	h = newHarness(t).reply(okJSON(`{}`))
	h.env.AppendFile = func(string, []byte) error { return errors.New("disk full") }
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{}`)
	if !strings.Contains(h.errOut.String(), "監査ログを書けません") {
		t.Errorf("警告が出ていません: %q", h.errOut.String())
	}
}

func TestAuditLogLocationAndOff(t *testing.T) {
	h := newHarness(t, withEnvVars(map[string]string{"MTB_AUDIT_LOG": "/work/.mtb/audit.jsonl"})).reply(okJSON(`{}`))
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{}`)
	if len(auditLines(t, h, "/work/.mtb/audit.jsonl")) != 1 {
		t.Error("MTB_AUDIT_LOG に書いていません")
	}
	if out := h.mustRun("history", "path"); strings.TrimSpace(out) != "/work/.mtb/audit.jsonl" {
		t.Errorf("history path = %q", out)
	}

	h = newHarness(t, withEnvVars(map[string]string{"MTB_AUDIT_LOG": "off"})).reply(okJSON(`{}`))
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{}`)
	for name := range h.files {
		if strings.Contains(name, "audit") {
			t.Errorf("off なのに書いています: %s", name)
		}
	}
}

func TestDryRunIsNotAudited(t *testing.T) {
	h := newHarness(t)
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{}`, "--dry-run")
	if _, ok := h.files[testAuditFile]; ok {
		t.Error("送っていないものを記録しています")
	}
}

func TestHistoryListsAndFilters(t *testing.T) {
	h := newHarness(t, withEnvVars(map[string]string{"MTB_AUDIT_READS": "1"}))
	h.reply(okJSON(`{}`), okJSON(`{}`), stub{status: 404, body: `{"error":{"message":"Not found"}}`, contentType: "application/json"})
	h.mustRun("sites", "entries", "list", "--site-id", "1")
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{}`, "--audit-note", "作業A")
	_ = h.run("sites", "entries", "get", "--site-id", "1", "--entry-id", "7")

	out := h.mustRun("history")
	if strings.Count(out, "\n") != 4 || !strings.Contains(out, "TIME") || !strings.Contains(out, "作業A") {
		t.Errorf("一覧が違います:\n%s", out)
	}

	out = h.mustRun("history", "--writes")
	if strings.Count(out, "\n") != 2 || !strings.Contains(out, "POST") {
		t.Errorf("--writes:\n%s", out)
	}
	out = h.mustRun("history", "list", "--status", "error", "--format", "json")
	var recs []api.AuditRecord
	if err := json.Unmarshal([]byte(out), &recs); err != nil || len(recs) != 1 || recs[0].Status != 404 {
		t.Errorf("--status error = %s (%v)", out, err)
	}
	out = h.mustRun("history", "--note", "作業A", "--limit", "1", "--format", "json")
	if err := json.Unmarshal([]byte(out), &recs); err != nil || len(recs) != 1 || recs[0].Note != "作業A" {
		t.Errorf("--note = %s", out)
	}
	out = h.mustRun("history", "--profile", "other", "--format", "json")
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("--profile で絞れていません: %s", out)
	}
	out = h.mustRun("history", "--since", "1h", "--method", "get", "--format", "json")
	if err := json.Unmarshal([]byte(out), &recs); err != nil || len(recs) != 2 {
		t.Errorf("--since/--method = %s", out)
	}
	if err := h.run("history", "--since", "きのう"); err == nil {
		t.Error("読めない時刻を受け付けています")
	}
}

func TestHistoryWithNoLog(t *testing.T) {
	h := newHarness(t)
	h.mustRun("history")
	if !strings.Contains(h.errOut.String(), "該当する記録はありません") {
		t.Errorf("stderr = %q", h.errOut.String())
	}
}
