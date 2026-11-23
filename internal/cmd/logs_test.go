package cmd

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/ideamans/mtb/internal/api"
)

var apiErrorClass = api.ErrorClass

func runLines(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(h.logfs.Files[testRunFile])), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("1行が JSON ではありません: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func events(lines []map[string]any) string {
	var ev []string
	for _, l := range lines {
		ev = append(ev, l["event"].(string))
	}
	return strings.Join(ev, ",")
}

func TestRunLogRecordsOneRunAndLinksToAudit(t *testing.T) {
	h := newHarness(t).reply(okJSON(`{"id":5}`))
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{"title":"x"}`)

	lines := runLines(t, h)
	if got := events(lines); got != "start,profile,http,end" {
		t.Fatalf("出来事 = %s", got)
	}
	for _, l := range lines {
		if l["run"] != "RUN1" || l["ts"] != "2023-11-14T22:13:20.000Z" {
			t.Errorf("run / ts が違います: %v", l)
		}
	}
	httpLine := lines[2]
	if httpLine["method"] != "POST" || httpLine["path"] != "/v7/sites/1/entries" || httpLine["status"] != float64(200) || httpLine["level"] != "info" {
		t.Errorf("http の行 = %v", httpLine)
	}
	if _, ok := httpLine["headers"]; ok {
		t.Error("debug でないのにヘッダを残しています")
	}
	if end := lines[3]; end["exit"] != float64(0) || end["level"] != "info" {
		t.Errorf("end = %v", end)
	}
	// 監査ログの行にも同じ ID
	if r := auditLines(t, h, testAuditFile)[0]; r.Run != "RUN1" {
		t.Errorf("監査ログの run = %q", r.Run)
	}
	out := h.mustRun("history", "--run", "RUN1", "--format", "json")
	if !strings.Contains(out, `"run": "RUN1"`) {
		t.Errorf("history --run = %s", out)
	}
	if out := h.mustRun("history", "--run", "NOPE", "--format", "json"); strings.TrimSpace(out) != "[]" {
		t.Errorf("history --run で絞れていません: %s", out)
	}
}

func TestRunLogClassifiesConnectionFailures(t *testing.T) {
	h := newHarness(t)
	h.doer.err = &net.DNSError{Err: "no such host", Name: "example.com", IsNotFound: true}
	if err := h.run("sites", "entries", "list", "--site-id", "1"); err == nil {
		t.Fatal("失敗するはず")
	}
	lines := runLines(t, h)
	var httpLine, end map[string]any
	for _, l := range lines {
		switch l["event"] {
		case "http":
			httpLine = l
		case "end":
			end = l
		}
	}
	if httpLine == nil || httpLine["class"] != "dns" || httpLine["level"] != "warn" {
		t.Errorf("http の行 = %v", httpLine)
	}
	if end == nil || end["level"] != "error" || end["exit"] == float64(0) {
		t.Errorf("end = %v", end)
	}

	// 既定の mtb logs は直近の警告とエラーだけ
	out := h.mustRun("logs")
	if !strings.Contains(out, "[dns]") || !strings.Contains(out, "exit=") || strings.Contains(out, "start") {
		t.Errorf("mtb logs:\n%s", out)
	}
	// --run なら全部
	out = h.mustRun("logs", "--run", "RUN1", "--format", "json")
	var all []map[string]any
	if err := json.Unmarshal([]byte(out), &all); err != nil || events(all) != "start,profile,http,end" {
		t.Errorf("logs --run = %s (%v)", out, err)
	}
	if err := h.run("logs", "--level", "loud"); err == nil {
		t.Error("知らない --level を受け付けています")
	}
}

func TestErrorClass(t *testing.T) {
	cases := map[string]error{
		"dns":     &net.DNSError{Err: "no such host"},
		"tcp":     &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		"timeout": &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}},
		"tls":     errors.New("tls: failed to verify certificate"),
		"other":   errors.New("something"),
	}
	for want, err := range cases {
		if got := apiErrorClass(err, 0); got != want {
			t.Errorf("%v → %s（%s のはず）", err, got, want)
		}
	}
	if apiErrorClass(nil, 404) != "http" || apiErrorClass(nil, 200) != "" {
		t.Error("ステータスでの判定が違います")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRunLogRedactsArgsAndDebugDetails(t *testing.T) {
	h := newHarness(t, withEnvVars(map[string]string{"MTB_LOG_LEVEL": "debug"}))
	h.reply(okJSON(`{"accessToken":"secret-access","sessionId":"secret-session","expiresIn":3600}`))
	h.mustRun("auth", "login", "--username", "alice", "--password", "pw-in-argv")
	raw := string(h.logfs.Files[testRunFile])
	for _, leak := range []string{"pw-in-argv", "secret-access", "secret-session", "token-1"} {
		if strings.Contains(raw, leak) {
			t.Errorf("%s が動作ログに残っています:\n%s", leak, raw)
		}
	}
	if !strings.Contains(raw, `"headers"`) || !strings.Contains(raw, `"body"`) {
		t.Errorf("debug なら送受信の詳細を残すはず:\n%s", raw)
	}

	// 認証済みの呼び出しの Authorization ヘッダも伏せる
	h = newHarness(t, withEnvVars(map[string]string{"MTB_LOG_LEVEL": "debug"})).reply(okJSON(`{}`))
	h.mustRun("sites", "entries", "list", "--site-id", "1")
	if raw := string(h.logfs.Files[testRunFile]); strings.Contains(raw, "token-1") || !strings.Contains(raw, "[redacted]") {
		t.Errorf("認証ヘッダが伏せられていません:\n%s", raw)
	}

	got := redactArgs([]string{"--password=x1", "--token", "x2", "--entry", `{"title":"t","password":"x3"}`, "--site-id", "1"})
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "x1") || strings.Contains(joined, "x2") || strings.Contains(joined, "x3") || !strings.Contains(joined, "--site-id 1") {
		t.Errorf("redactArgs = %v", got)
	}
}

func TestLogsOffAndCompletionAreQuiet(t *testing.T) {
	h := newHarness(t, withEnvVars(map[string]string{"MTB_LOG": "off"})).reply(okJSON(`{"id":1}`))
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{}`)
	if _, ok := h.logfs.Files[testRunFile]; ok {
		t.Error("MTB_LOG=off なのに動作ログを書いています")
	}
	if _, ok := h.logfs.Files[testAuditFile]; !ok {
		t.Error("動作ログを止めても監査ログは残るはず")
	}

	h = newHarness(t)
	_ = h.run("__complete", "sites", "")
	if len(h.logfs.Files) != 0 {
		t.Errorf("補完の呼び出しを残しています: %v", keys(h.logfs.Files))
	}
}

func TestHistoryReadsCompressedDays(t *testing.T) {
	h := newHarness(t)
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(`{"ts":"2023-11-10T01:00:00.000Z","method":"POST","path":"/v7/sites","status":200,"write":true,"run":"OLD"}` + "\n"))
	_ = w.Close()
	h.logfs.Files["mem:logs/audit/2023-11-10.jsonl.gz"] = b.Bytes()
	h.logfs.Files["mem:logs/audit/2023-11-13.jsonl"] = []byte(`{"ts":"2023-11-13T01:00:00.000Z","method":"PUT","path":"/v7/sites/1","status":200,"write":true,"run":"NEW"}` + "\n")
	out := h.mustRun("history", "--format", "json")
	var recs []map[string]any
	if err := json.Unmarshal([]byte(out), &recs); err != nil || len(recs) != 2 || recs[0]["run"] != "OLD" || recs[1]["run"] != "NEW" {
		t.Errorf("gz と素の両方を古い順に読むはず: %s", out)
	}
	if out := h.mustRun("history", "--file", "mem:logs/audit/2023-11-10.jsonl.gz", "--format", "json"); !strings.Contains(out, "OLD") || strings.Contains(out, "NEW") {
		t.Errorf("--file で .gz を読めていません: %s", out)
	}
}

func TestStartupMaintenanceCompressesOldDays(t *testing.T) {
	h := newHarness(t)
	h.logfs.Files["mem:logs/run/2023-11-01.jsonl"] = []byte("{}\n")
	h.logfs.Files["mem:logs/run/2023-10-01.jsonl.gz"] = []byte("x")
	h.mustRun("history")
	if _, ok := h.logfs.Files["mem:logs/run/2023-11-01.jsonl.gz"]; !ok {
		t.Errorf("起動時に古い日を圧縮していません: %v", keys(h.logfs.Files))
	}
	if _, ok := h.logfs.Files["mem:logs/run/2023-10-01.jsonl.gz"]; ok {
		t.Error("30日より前を消していません")
	}
	// ほかの mtb が片付けている間は触らない
	h = newHarness(t)
	h.logfs.Files["mem:logs/run/2023-11-01.jsonl"] = []byte("{}\n")
	release := h.logfs.Hold("mem:logs/.maintenance.lock")
	defer release()
	h.mustRun("history")
	if _, ok := h.logfs.Files["mem:logs/run/2023-11-01.jsonl"]; !ok {
		t.Error("ロック中に片付けています")
	}
}
