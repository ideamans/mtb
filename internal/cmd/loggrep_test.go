package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHistoryGrepAndCount(t *testing.T) {
	h := newHarness(t)
	h.logfs.Files[testAuditFile] = []byte(strings.Join([]string{
		`{"ts":"2023-11-14T01:00:00.000Z","profile":"prod","method":"POST","path":"/v7/sites/1/entries","params":{"entry":"{\"title\":\"春のキャンペーン\",\"body\":\"<p>x</p>\"}"},"status":200,"write":true,"run":"A"}`,
		`{"ts":"2023-11-14T02:00:00.000Z","profile":"stg","method":"PUT","path":"/v7/sites/1/entries/3","params":{"entry":"{\"title\":\"夏\"}"},"status":409,"error":"Conflict happened","write":true,"run":"B"}`,
		`{"ts":"2023-11-14T03:00:00.000Z","profile":"prod","method":"DELETE","path":"/v7/sites/1/entries/4","status":200,"note":"Spring cleanup","write":true,"run":"C"}`,
	}, "\n") + "\n")

	runs := func(args ...string) string {
		t.Helper()
		out := h.mustRun(append([]string{"history", "--format", "json"}, args...)...)
		var recs []map[string]any
		if err := json.Unmarshal([]byte(out), &recs); err != nil {
			t.Fatalf("JSON の配列ではありません: %s", out)
		}
		var ids []string
		for _, r := range recs {
			ids = append(ids, r["run"].(string))
		}
		return strings.Join(ids, ",")
	}
	if got := runs("--grep", "キャンペーン"); got != "A" {
		t.Errorf("パラメータの中の語 = %s", got)
	}
	if got := runs("--grep", "<p>"); got != "A" {
		t.Errorf("< > を含む語（JSON では \\u003c）= %s", got)
	}
	if got := runs("--grep", "conflict"); got != "B" {
		t.Errorf("大文字小文字を区別しない・error の中 = %s", got)
	}
	if got := runs("--grep", "spring entries"); got != "C" {
		t.Errorf("空白区切りは AND = %s", got)
	}
	if got := runs("--grep", "entries", "--grep", "409"); got != "B" {
		t.Errorf("繰り返しは AND・数値にも当たる = %s", got)
	}
	if got := runs("--profile", "prod"); got != "A,C" {
		t.Errorf("--profile = %s", got)
	}
	if out := strings.TrimSpace(h.mustRun("history", "--profile", "prod", "--count", "--limit", "1")); out != "2" {
		t.Errorf("--count は --limit を無視して件数だけ = %q", out)
	}
	// JSON はファイルの行と同じキーで出る
	out := h.mustRun("history", "--format", "json", "--grep", "夏")
	for _, k := range []string{`"profile"`, `"params"`, `"error"`, `"run"`, `"write"`} {
		if !strings.Contains(out, k) {
			t.Errorf("JSON に %s がありません: %s", k, out)
		}
	}
}

func TestLogsProfileSelectsWholeRunsAndGrep(t *testing.T) {
	// 読むコマンド自身の実行を数えないよう、書き込みは止める（studio のサーバと同じ使い方）
	h := newHarness(t, withEnvVars(map[string]string{"MTB_LOG": "off"}))
	h.logfs.Files[testRunFile] = []byte(strings.Join([]string{
		`{"ts":"2023-11-14T20:00:00.000Z","run":"A","level":"info","event":"start","msg":"","profile":""}`,
		`{"ts":"2023-11-14T20:00:00.100Z","run":"A","level":"info","event":"profile","msg":"","profile":"prod"}`,
		`{"ts":"2023-11-14T20:00:00.200Z","run":"A","level":"warn","event":"http","msg":"dial tcp: connection refused","class":"tcp","profile":"prod"}`,
		`{"ts":"2023-11-14T20:00:00.300Z","run":"A","level":"error","event":"end","msg":"failed","exit":6,"profile":"prod"}`,
		`{"ts":"2023-11-14T21:00:00.000Z","run":"B","level":"info","event":"start","msg":"","profile":""}`,
		`{"ts":"2023-11-14T21:00:00.100Z","run":"B","level":"warn","event":"http","msg":"TLS handshake timeout","class":"tls","profile":"stg"}`,
	}, "\n") + "\n")

	get := func(args ...string) []map[string]any {
		t.Helper()
		out := h.mustRun(append([]string{"logs", "--format", "json"}, args...)...)
		var es []map[string]any
		if err := json.Unmarshal([]byte(out), &es); err != nil {
			t.Fatalf("JSON の配列ではありません: %s", out)
		}
		return es
	}
	if es := get("--profile", "prod", "--level", "all"); len(es) != 4 || es[0]["event"] != "start" {
		t.Errorf("--profile は実行ごと（解決前の start も）残すはず: %v", es)
	}
	if es := get("--profile", "prod"); len(es) != 2 {
		t.Errorf("既定は warn 以上: %v", es)
	}
	if es := get("--grep", "handshake"); len(es) != 1 || es[0]["run"] != "B" {
		t.Errorf("--grep = %v", es)
	}
	if out := strings.TrimSpace(h.mustRun("logs", "--level", "all", "--count")); out != "6" {
		t.Errorf("--level all --count = %q", out)
	}
	if err := h.run("logs", "--level", "loud"); err == nil {
		t.Error("知らない --level を受け付けています")
	}
}

func TestRunLogLinesCarryResolvedProfile(t *testing.T) {
	h := newHarness(t).reply(okJSON(`{"id":5}`))
	h.mustRun("sites", "entries", "create", "--site-id", "1", "--entry", `{}`)
	lines := runLines(t, h)
	if lines[0]["profile"] != "" {
		t.Errorf("start はプロファイル解決前なので空のはず: %v", lines[0])
	}
	for _, l := range lines[1:] {
		if l["profile"] != "prod" {
			t.Errorf("解決後の行には profile が付くはず: %v", l)
		}
	}
}

func TestReadingWorksWhenWritingIsOff(t *testing.T) {
	h := newHarness(t, withEnvVars(map[string]string{"MTB_LOG": "off", "MTB_AUDIT_LOG": "off"}))
	h.logfs.Files[testAuditFile] = []byte(`{"ts":"2023-11-14T01:00:00.000Z","method":"POST","path":"/v7/sites","status":200,"write":true,"run":"A"}` + "\n")
	h.logfs.Files[testRunFile] = []byte(`{"ts":"2023-11-14T22:00:00.000Z","run":"A","level":"error","event":"end","msg":"x"}` + "\n")
	if out := strings.TrimSpace(h.mustRun("history", "--count")); out != "1" {
		t.Errorf("MTB_AUDIT_LOG=off でも読めるはず: %q", out)
	}
	if out := strings.TrimSpace(h.mustRun("logs", "--count")); out != "1" {
		t.Errorf("MTB_LOG=off でも読めるはず: %q", out)
	}
	// 読んだことで書き足していない
	if strings.Count(string(h.logfs.Files[testRunFile]), "\n") != 1 || strings.Count(string(h.logfs.Files[testAuditFile]), "\n") != 1 {
		t.Error("off なのに読むコマンドが書き足しています")
	}
}
