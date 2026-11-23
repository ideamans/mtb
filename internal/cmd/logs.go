package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ideamans/mtb/internal/api"
	"github.com/ideamans/mtb/internal/i18n"
	"github.com/ideamans/mtb/internal/logs"
)

// 手元のログ（監査ログと動作ログ）。
//
// 置き場は設定ディレクトリの logs/ で、設定とログが1か所にまとまります。
// MTB_LOG_DIR で logs/ だけを別の場所へ移せます（mtb studio はセッションの作業場所へ置く）。
// 圧縮・削除の日数は internal/logs の Policy にあり、いまは設定の口を設けていません。
// ふだん利用者が見るものではなく、エージェントとサポートが引くためのものです。

const (
	// logDirEnv は logs/ の場所を変えます。
	logDirEnv = "MTB_LOG_DIR"
	// runLogEnv が off なら動作ログを残しません。
	runLogEnv = "MTB_LOG"
	// logLevelEnv は動作ログに残す下限（debug|info|warn|error。既定 info）。
	logLevelEnv = "MTB_LOG_LEVEL"
	// runArgLimit は動作ログに残すコマンドラインの1語の長さの上限です。
	runArgLimit = 200
)

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// logStore は手元のログです。置き場が決まらなければ nil。
func (a *App) logStore() *logs.Logs {
	a.logOnce.Do(func() {
		if a.env.LogFS == nil {
			return
		}
		dir := strings.TrimSpace(a.env.getenv(logDirEnv))
		if dir == "" {
			if a.env.Store == nil {
				return
			}
			dir = a.env.Store.Location("logs")
		}
		a.logs = logs.New(a.env.LogFS, dir, a.env.now)
	})
	return a.logs
}

// logWarn はログが書けないことを1回だけ知らせます（コマンドは止めない）。
func (a *App) logWarn(err error) {
	if err == nil || a.logWarned {
		return
	}
	a.logWarned = true
	fmt.Fprintf(a.env.Err, i18n.T("警告: ログを書けません: %v\n", "warning: cannot write the log: %v\n"), err)
}

func (a *App) runLogOn() bool {
	return !strings.EqualFold(strings.TrimSpace(a.env.getenv(runLogEnv)), "off") && a.logStore() != nil
}

func (a *App) logLevel() int {
	if r, ok := levelRank[strings.ToLower(strings.TrimSpace(a.env.getenv(logLevelEnv)))]; ok {
		return r
	}
	return levelRank["info"]
}

// runLogger は動作ログの書き手です（api.RunLogger）。
type runLogger struct{ a *App }

func (r runLogger) Debug() bool { return r.a.logLevel() <= levelRank["debug"] }

func (r runLogger) Log(level, event, msg string, fields map[string]any) {
	a := r.a
	if !a.runLogOn() || levelRank[level] < a.logLevel() {
		return
	}
	// どの行にも、その時点で解決済みのプロファイル名を付ける（解決前の start などは ""）
	f := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		f[k] = v
	}
	f["profile"] = a.runProfile
	a.logWarn(a.logStore().Append(logs.Run, runLine(a.env.now(), a.runID, level, event, msg, f)))
}

// runLine は動作ログの1行です。先頭に ts run level event msg を置き、残りは名前の順。
func runLine(t time.Time, run, level, event, msg string, fields map[string]any) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	put := func(k string, v any) {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(v)
		if err != nil {
			vb, _ = json.Marshal(fmt.Sprint(v))
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	put("ts", t.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	put("run", run)
	put("level", level)
	put("event", event)
	put("msg", msg)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		switch k {
		case "ts", "run", "level", "event", "msg":
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		put(k, fields[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (a *App) log(level, event, msg string, fields map[string]any) {
	runLogger{a}.Log(level, event, msg, fields)
}

// redactArgs はコマンドラインを残せる形にします。秘密らしい名前のフラグの値は伏せ、
// 長い値（--entry の JSON など）は切り詰めます。
func redactArgs(args []string) []string {
	out := make([]string, 0, len(args))
	hideNext := false
	for _, s := range args {
		switch {
		case hideNext:
			hideNext = false
			if !strings.HasPrefix(s, "-") {
				out = append(out, "[redacted]")
				continue
			}
		}
		if strings.HasPrefix(s, "-") && s != "-" {
			name := strings.TrimLeft(s, "-")
			if k, v, ok := strings.Cut(name, "="); ok {
				if api.IsSecretName(k) {
					out = append(out, s[:len(s)-len(v)]+"[redacted]")
				} else {
					out = append(out, s[:len(s)-len(v)]+api.RedactValue(v, runArgLimit))
				}
				continue
			}
			out = append(out, s)
			hideNext = api.IsSecretName(name)
			continue
		}
		out = append(out, api.RedactValue(s, runArgLimit))
	}
	return out
}

// beginRun は実行の始まりを残し、1日1回の片付けをします。
func (a *App) beginRun(args []string) {
	a.started = a.env.now()
	if a.env.NewRunID != nil {
		a.runID = a.env.NewRunID()
	} else {
		a.runID = logs.NewRunID(a.started)
	}
	// シェル補完は打鍵のたびに呼ばれるので残さない
	if len(args) > 0 && strings.HasPrefix(args[0], "__complete") {
		a.quietRun = true
		return
	}
	if l := a.logStore(); l != nil {
		if _, err := l.Maintain(); err != nil {
			a.logWarn(err)
		}
	}
	a.log("info", "start", "", map[string]any{"version": Version, "args": redactArgs(args)})
	for _, e := range a.loadedExtensions {
		a.log("debug", "extension", "", map[string]any{"name": e.Name, "version": e.Version, "source": e.Source})
	}
	for _, err := range a.extensionErrors {
		a.log("warn", "extension", err.Error(), nil)
	}
}

// endRun は終了コードと所要時間を残します。
func (a *App) endRun(err error) {
	if a.quietRun {
		return
	}
	code := ExitOK
	level, msg := "info", ""
	if err != nil {
		code = ExitCode(err)
		level, msg = "error", api.RedactValue(err.Error(), api.AuditValueLimit)
	}
	a.log(level, "end", msg, map[string]any{"exit": code, "ms": max(a.env.now().Sub(a.started).Milliseconds(), 0)})
}

// --- mtb logs ---

// runEntry は動作ログの1行を読んだものです。
type runEntry struct {
	TS    string `json:"ts"`
	Run   string `json:"run"`
	Level string `json:"level"`
	Event string `json:"event"`
	Msg   string `json:"msg"`
	// 一覧の1行にまとめるための項目
	Method  string `json:"method"`
	Path    string `json:"path"`
	Status  int    `json:"status"`
	Class   string `json:"class"`
	Exit    *int   `json:"exit"`
	Profile string `json:"profile"`

	raw json.RawMessage
}

type logsFilter struct {
	since, level, run string
	limit             int
	grep              []string
	count             bool
}

func (a *App) newLogsCmd() *cobra.Command {
	var f logsFilter
	cmd := &cobra.Command{
		Use:   "logs",
		Short: i18n.T("mtb の動作ログを見る（接続の失敗・送り直し・終了コード）", "Show mtb's operation log (connection failures, retries, exit codes)"),
		Long: i18n.T(
			"mtb は実行のたびに、何が起きたかを手元の動作ログ（JSON Lines）に残します。\n"+
				"起動・接続先の解決・送信の試み（失敗の種類 dns / tcp / tls / timeout / http）・送り直し・終了コード。\n"+
				"既定では直近24時間の警告とエラーだけを出します。--run で1回の実行をすべて追えます\n"+
				"（同じ ID が監査ログ mtb history にも入っています）。\n\n"+
				"置き場は設定ディレクトリの logs/run/（MTB_LOG_DIR で logs/ ごと移せます。MTB_LOG=off で残さない）。\n"+
				"MTB_LOG_LEVEL=debug なら、伏せたヘッダと切り詰めた本文も残します。\n"+
				"2日以上前のファイルは gzip で圧縮し、30日より前のものは自動で消します。",
			"mtb records what happened on every run in a local operation log (JSON Lines):\n"+
				"start, connection target, each request attempt (failure class dns / tcp / tls / timeout / http), retries and the exit code.\n"+
				"By default this shows warnings and errors from the last 24 hours. Use --run to follow one run in full\n"+
				"(the same ID appears in the audit log, mtb history).\n\n"+
				"It lives under logs/run/ in the config directory (move logs/ with MTB_LOG_DIR; MTB_LOG=off disables it).\n"+
				"With MTB_LOG_LEVEL=debug it also keeps redacted headers and truncated bodies.\n"+
				"Files older than 2 days are gzipped and files older than 30 days are deleted automatically."),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if fl := cmd.Flags().Lookup("profile"); fl != nil && fl.Changed {
				profile = a.g.profile
			}
			return a.runLogs(f, profile)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.since, "since", "", i18n.T("この時刻以降（既定 24h。--run を付けたら全期間）", "from this time (default 24h; all time with --run)"))
	fl.StringVar(&f.level, "level", "", i18n.T("この重さ以上 debug|info|warn|error|all（既定 warn。--run を付けたら all）", "this level and above: debug|info|warn|error|all (default warn; all with --run)"))
	fl.StringVar(&f.run, "run", "", i18n.T("1回の実行の ID（mtb history にも出ます）", "the ID of one run (also shown by mtb history)"))
	fl.IntVar(&f.limit, "limit", 100, i18n.T("新しい方から何件出すか（0 で全部）", "how many of the newest to show (0 for all)"))
	fl.StringArrayVar(&f.grep, "grep", nil, grepHelp())
	fl.BoolVar(&f.count, "count", false, countHelp())
	// --profile（全体のフラグ）は、その接続先を使った実行だけに絞る
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: i18n.T("動作ログの置き場を表示する", "Print where the operation log is kept"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !a.runLogOn() {
				fmt.Fprintln(a.env.Err, i18n.T("動作ログは残していません（MTB_LOG=off）", "the operation log is off (MTB_LOG=off)"))
				return nil
			}
			fmt.Fprintln(a.env.Out, a.logStore().KindDir(logs.Run))
			return nil
		},
	})
	return cmd
}

func (a *App) runLogs(f logsFilter, profile string) error {
	now := a.env.now()
	sinceArg, levelArg := f.since, f.level
	if f.run == "" {
		if sinceArg == "" {
			sinceArg = "24h"
		}
		if levelArg == "" {
			levelArg = "warn"
		}
	}
	since, err := parseHistoryTime(sinceArg, now)
	if err != nil {
		return &usageError{fmt.Errorf("--since: %w", err)}
	}
	minLevel := 0
	if levelArg != "" && !strings.EqualFold(levelArg, "all") {
		r, ok := levelRank[strings.ToLower(levelArg)]
		if !ok {
			return usagef("%s", i18n.T("--level は debug|info|warn|error|all のどれかです", "--level must be one of debug|info|warn|error|all"))
		}
		minLevel = r
	}
	words := grepWords(f.grep)
	var entries []runEntry
	// --profile: そのプロファイルを使った実行（run）を集め、実行ごと残す（start の行はまだ解決前なので）
	profileRuns := map[string]bool{}
	if l := a.logStore(); l != nil {
		err = l.Each(logs.Run, func(line []byte) {
			var e runEntry
			if json.Unmarshal(line, &e) != nil {
				return
			}
			if profile != "" && e.Profile == profile {
				profileRuns[e.Run] = true
			}
			if !grepMatch(line, words) {
				return
			}
			if f.run != "" && e.Run != f.run {
				return
			}
			if levelRank[e.Level] < minLevel {
				return
			}
			if !since.IsZero() {
				if t, err := time.Parse(time.RFC3339Nano, e.TS); err != nil || t.Before(since) {
					return
				}
			}
			e.raw = append(json.RawMessage{}, line...)
			entries = append(entries, e)
		})
		if err != nil {
			return err
		}
	}
	if profile != "" {
		kept := entries[:0]
		for _, e := range entries {
			if profileRuns[e.Run] {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].TS < entries[j].TS })
	if f.count {
		_, err = fmt.Fprintln(a.env.Out, len(entries))
		return err
	}
	if f.limit > 0 && len(entries) > f.limit {
		entries = entries[len(entries)-f.limit:]
	}

	if a.g.format == "json" {
		raws := make([]json.RawMessage, 0, len(entries))
		for _, e := range entries {
			raws = append(raws, e.raw)
		}
		var buf []byte
		if a.g.compact {
			buf, err = json.Marshal(raws)
		} else {
			buf, err = json.MarshalIndent(raws, "", "  ")
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.env.Out, string(buf))
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(a.env.Err, i18n.T("該当する記録はありません", "no matching records"))
		return nil
	}
	w := tabwriter.NewWriter(a.env.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tLEVEL\tEVENT\tRUN\tDETAIL")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.TS, e.Level, e.Event, e.Run, e.detail())
	}
	return w.Flush()
}

// detail は一覧の1行の説明です。
func (e runEntry) detail() string {
	var parts []string
	if e.Method != "" {
		parts = append(parts, e.Method+" "+e.Path)
	}
	if e.Status != 0 {
		parts = append(parts, fmt.Sprint(e.Status))
	}
	if e.Class != "" {
		parts = append(parts, "["+e.Class+"]")
	}
	if e.Exit != nil {
		parts = append(parts, fmt.Sprintf("exit=%d", *e.Exit))
	}
	if e.Msg != "" {
		parts = append(parts, strings.ReplaceAll(e.Msg, "\n", " "))
	}
	return strings.Join(parts, " ")
}

// logState は App に持たせるログの状態です。
type logState struct {
	logOnce   sync.Once
	logs      *logs.Logs
	logWarned bool
	runID     string
	started   time.Time
	quietRun  bool
	// runProfile は解決したプロファイル名です（動作ログの各行に付ける）
	runProfile string
}
