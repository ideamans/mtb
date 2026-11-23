package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ideamans/mtb/internal/api"
	"github.com/ideamans/mtb/internal/i18n"
	"github.com/ideamans/mtb/internal/logs"
)

// ローカルの監査ログ。
//
// mtb は送った Data API の呼び出しを、手元の JSON Lines に1行ずつ残します。
// MtbPro（サーバ側の監査ログ）は MT に届いた全員の操作を残しますが、素の MT には
// それが無いので、「この mtb で何をしたか」は手元にしか残せません。
// 行の形は MtbPro に合わせてあり、mtb studio もこのファイルを読みます。

const (
	// auditLogEnv が off なら監査ログを残しません。
	auditLogEnv = "MTB_AUDIT_LOG"
	// auditReadsEnv が 1 なら読み取り（GET）も残します。既定は書き込みだけ。
	auditReadsEnv = "MTB_AUDIT_READS"
)

// auditOn は監査ログを残すかどうかです。
func (a *App) auditOn() bool {
	return !strings.EqualFold(strings.TrimSpace(a.env.getenv(auditLogEnv)), "off") && a.logStore() != nil
}

func (a *App) auditReads() bool {
	v := strings.TrimSpace(a.env.getenv(auditReadsEnv))
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// fileAuditor は監査ログを書く api.Auditor です。
//
// **ログが書けなくてもコマンドは止めない。** 監査ログのためにサイトの作業が
// できなくなるのは本末転倒。ただし黙らず、1回だけ標準エラーに出します。
type fileAuditor struct {
	a       *App
	profile string
	user    func() string
}

func (f *fileAuditor) Record(r api.AuditRecord) {
	if !r.Write && !f.a.auditReads() {
		return
	}
	if !f.a.auditOn() {
		return
	}
	r.Profile = f.profile
	r.Run = f.a.runID
	if f.user != nil {
		r.User = f.user()
	}
	line, err := json.Marshal(r)
	if err == nil {
		err = f.a.logStore().Append(logs.Audit, line)
	}
	f.a.logWarn(err)
}

// newHistoryCmd は mtb history です。
func (a *App) newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: i18n.T("この mtb が送った Data API の呼び出しを見る（ローカルの監査ログ）", "Show the Data API calls this mtb has sent (the local audit log)"),
		Long: i18n.T(
			"mtb は送った Data API の呼び出しを手元の監査ログ（JSON Lines）に1行ずつ残します。\n"+
				"既定は書き込み（GET 以外）だけで、MTB_AUDIT_READS=1 なら読み取りも残します。\n"+
				"置き場は設定ディレクトリの logs/audit/<日付>.jsonl です（MTB_LOG_DIR で logs/ ごと移せます。MTB_AUDIT_LOG=off で残さない）。\n"+
				"2日以上前のファイルは gzip で圧縮し、30日より前のものは自動で消します。\n"+
				"各行の run は実行の ID で、mtb logs --run で同じ実行の接続の様子を追えます。\n"+
				"パスワード・トークン・セッションの値は伏せ、長い値は切り詰めて SHA-256 を添えます。\n\n"+
				"これは「この端末の mtb が送ったもの」だけです。チーム全員の操作をサーバ側に残すのは\n"+
				"MtbPro（mtb-pro 拡張の audit-logs で読む）の役目です。",
			"mtb records each Data API call it sends in a local audit log (JSON Lines).\n"+
				"By default only writes (anything but GET) are kept; set MTB_AUDIT_READS=1 to keep reads too.\n"+
				"It lives in logs/audit/<date>.jsonl under the config directory (move logs/ with MTB_LOG_DIR; MTB_AUDIT_LOG=off disables it).\n"+
				"Files older than 2 days are gzipped and files older than 30 days are deleted automatically.\n"+
				"Each line's run is the ID of the run; mtb logs --run shows what happened on the connection in that run.\n"+
				"Passwords, tokens and session values are redacted; long values are truncated with their SHA-256.\n\n"+
				"This only covers what mtb on this machine sent. Keeping everyone's operations on the server\n"+
				"is what MtbPro does (read it with audit-logs from the mtb-pro extension)."),
		Args: cobra.NoArgs,
	}
	list := a.newHistoryListCmd()
	// mtb history だけで一覧を出す（list と同じフラグを受ける）
	cmd.Flags().AddFlagSet(list.Flags())
	cmd.RunE = list.RunE
	cmd.AddCommand(list, a.newHistoryPathCmd())
	return cmd
}

type historyFilter struct {
	since, until string
	method       string
	path         string
	status       string
	note         string
	writes       bool
	limit        int
	file         string
	run          string
}

func (a *App) newHistoryListCmd() *cobra.Command {
	var f historyFilter
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   i18n.T("監査ログの一覧（新しいものが下）", "List the audit log (newest last)"),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if fl := cmd.Flags().Lookup("profile"); fl != nil && fl.Changed {
				profile = a.g.profile
			}
			return a.runHistory(f, profile)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.since, "since", "", i18n.T("この時刻以降（2026-11-23、RFC 3339、または 24h・7d のような長さ）", "from this time (2026-11-23, RFC 3339, or a duration like 24h or 7d)"))
	fl.StringVar(&f.until, "until", "", i18n.T("この時刻より前（--since と同じ書式）", "before this time (same formats as --since)"))
	fl.StringVar(&f.method, "method", "", i18n.T("メソッドで絞る（POST,PUT のように複数可）", "filter by method (several allowed, like POST,PUT)"))
	fl.StringVar(&f.path, "path", "", i18n.T("パスに含まれる文字列で絞る", "filter by a substring of the path"))
	fl.StringVar(&f.status, "status", "", i18n.T("ステータスで絞る（200、4xx、error＝400以上か送れなかったもの）", "filter by status (200, 4xx, or error = 400 and above or not sent)"))
	fl.StringVar(&f.note, "note", "", i18n.T("作業の説明（--audit-note）に含まれる文字列で絞る", "filter by a substring of the work description (--audit-note)"))
	fl.BoolVar(&f.writes, "writes", false, i18n.T("書き込みだけ（読み取りを残している場合）", "writes only (when reads are being kept)"))
	fl.IntVar(&f.limit, "limit", 50, i18n.T("新しい方から何件出すか（0 で全部）", "how many of the newest to show (0 for all)"))
	fl.StringVar(&f.file, "file", "", i18n.T("読むファイル（.jsonl か .jsonl.gz。既定は logs/audit/ の全部）", "file to read (.jsonl or .jsonl.gz; default: everything under logs/audit/)"))
	fl.StringVar(&f.run, "run", "", i18n.T("1回の実行の ID で絞る（mtb logs --run と同じ ID）", "filter by the ID of one run (the same ID as mtb logs --run)"))
	return cmd
}

func (a *App) newHistoryPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: i18n.T("監査ログの置き場を表示する", "Print where the audit log is kept"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !a.auditOn() {
				fmt.Fprintln(a.env.Err, i18n.T("監査ログは残していません（MTB_AUDIT_LOG=off）", "the audit log is off (MTB_AUDIT_LOG=off)"))
				return nil
			}
			fmt.Fprintln(a.env.Out, a.logStore().KindDir(logs.Audit))
			return nil
		},
	}
}

func (a *App) runHistory(f historyFilter, profile string) error {
	now := a.env.now()
	since, err := parseHistoryTime(f.since, now)
	if err != nil {
		return &usageError{fmt.Errorf("--since: %w", err)}
	}
	until, err := parseHistoryTime(f.until, now)
	if err != nil {
		return &usageError{fmt.Errorf("--until: %w", err)}
	}
	methods := map[string]bool{}
	for _, m := range strings.Split(f.method, ",") {
		if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
			methods[m] = true
		}
	}

	var recs []api.AuditRecord
	each := func(line []byte) {
		var r api.AuditRecord
		if json.Unmarshal(line, &r) != nil {
			return // 途中で切れた行などは飛ばす
		}
		if keepRecord(r, f, methods, profile, since, until) {
			recs = append(recs, r)
		}
	}
	if l := a.logStore(); l != nil || f.file != "" {
		if l == nil {
			l = logs.New(a.env.LogFS, "", a.env.now)
		}
		var err error
		if f.file != "" {
			err = l.EachInFile(f.file, each)
		} else {
			err = l.Each(logs.Audit, each)
		}
		if err != nil {
			return err
		}
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].TS < recs[j].TS })
	if f.limit > 0 && len(recs) > f.limit {
		recs = recs[len(recs)-f.limit:]
	}

	if a.g.format == "json" {
		if recs == nil {
			recs = []api.AuditRecord{}
		}
		var buf []byte
		if a.g.compact {
			buf, err = json.Marshal(recs)
		} else {
			buf, err = json.MarshalIndent(recs, "", "  ")
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.env.Out, string(buf))
		return err
	}
	if len(recs) == 0 {
		fmt.Fprintln(a.env.Err, i18n.T("該当する記録はありません", "no matching records"))
		return nil
	}
	w := tabwriter.NewWriter(a.env.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tPROFILE\tMETHOD\tSTATUS\tMS\tPATH\tRUN\tNOTE")
	for _, r := range recs {
		status := strconv.Itoa(r.Status)
		if r.Status == 0 {
			status = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", r.TS, dash(r.Profile), r.Method, status, r.MS, r.Path, dash(r.Run), r.Note)
	}
	return w.Flush()
}

func keepRecord(r api.AuditRecord, f historyFilter, methods map[string]bool, profile string, since, until time.Time) bool {
	if f.writes && !r.Write {
		return false
	}
	if f.run != "" && r.Run != f.run {
		return false
	}
	if len(methods) > 0 && !methods[r.Method] {
		return false
	}
	if f.path != "" && !strings.Contains(r.Path, f.path) {
		return false
	}
	if f.note != "" && !strings.Contains(r.Note, f.note) {
		return false
	}
	if profile != "" && r.Profile != profile {
		return false
	}
	if f.status != "" && !statusMatches(r, f.status) {
		return false
	}
	if !since.IsZero() || !until.IsZero() {
		t, err := time.Parse(time.RFC3339Nano, r.TS)
		if err != nil {
			return false
		}
		if !since.IsZero() && t.Before(since) {
			return false
		}
		if !until.IsZero() && !t.Before(until) {
			return false
		}
	}
	return true
}

func statusMatches(r api.AuditRecord, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	switch {
	case want == "error":
		return r.Status == 0 || r.Status >= 400
	case len(want) == 3 && strings.HasSuffix(want, "xx"):
		return r.Status/100 == int(want[0]-'0')
	}
	return strconv.Itoa(r.Status) == want
}

// parseHistoryTime は日付・RFC 3339・長さ（24h、7d）を読みます。空ならゼロ値。
func parseHistoryTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New(i18n.T("時刻を読めません（2026-11-23、RFC 3339、24h・7d のどれか）", "cannot read the time (use 2026-11-23, RFC 3339, or 24h / 7d)"))
}
