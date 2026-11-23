package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ideamans/mtb/internal/i18n"
	"github.com/ideamans/mtb/internal/selfupdate"
)

// installGuideURL はインストールの手順のページです。更新元が変わって mtb upgrade が
// 働かなくなったら、ここから入れ直してもらいます。**この URL は変えない約束です。**
const installGuideURL = "https://www.mtbcli.com/guide/install"

// upgradeReport は mtb upgrade の結果です（--format json で出す形）。
type upgradeReport struct {
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	// Status は up-to-date | available | upgraded | newer-local | package-manager | source-build
	Status     string   `json:"status"`
	Manager    string   `json:"manager,omitempty"`
	Commands   []string `json:"commands,omitempty"`
	Executable string   `json:"executable,omitempty"`
}

func (a *App) newUpgradeCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: i18n.T("mtb を最新版に入れ替える（GitHub のリリースから）", "Replace mtb with the latest release (from GitHub)"),
		Long: i18n.T(
			"GitHub のリリース（ideamans/mtb）の最新版を調べ、手元より新しければ入れ替えます。\n"+
				"配布物はチェックサム（checksums.txt の SHA-256）を照らしてから、実行ファイルの隣に書いて\n"+
				"rename で置き換えます。--check は調べるだけで、何も書き換えません。\n\n"+
				"apt・yum・Homebrew・Scoop・Chocolatey で入れたものは書き換えず、打つべきコマンドを案内します。\n"+
				"ソースからのビルドは入れ替えません。\n"+
				"更新元に届かない・見つからない・照合できないときは何も変えずに終わります。その場合は\n"+
				installGuideURL+" の手順で入れ直してください。\n\n"+
				"自動で確認したり通知したりはしません。",
			"Look up the latest release of ideamans/mtb on GitHub and, if it is newer, replace this mtb.\n"+
				"The archive is checked against checksums.txt (SHA-256), written next to the executable\n"+
				"and swapped in with a rename. --check only looks; it changes nothing.\n\n"+
				"Installs from apt, yum, Homebrew, Scoop or Chocolatey are left alone; mtb prints the command to run.\n"+
				"Builds from source are not replaced.\n"+
				"If the update source cannot be reached, found or verified, nothing is changed;\n"+
				"reinstall following "+installGuideURL+".\n\n"+
				"mtb never checks for updates or notifies you on its own."),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runUpgrade(check) },
	}
	cmd.Flags().BoolVar(&check, "check", false, i18n.T("最新版を調べるだけで、入れ替えない", "only look up the latest version; do not replace anything"))
	return cmd
}

// sourceError は更新元の失敗に、入れ直しの案内を添えます。
func sourceError(err error) error {
	return fmt.Errorf(i18n.T(
		"更新元から最新版を取得できませんでした（何も変更していません）: %w\n%s の手順で入れ直してください",
		"could not get the latest release from the update source (nothing was changed): %w\nreinstall following %s"),
		err, installGuideURL)
}

func (a *App) runUpgrade(check bool) error {
	goos, goarch := a.env.GOOS, a.env.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	rep := upgradeReport{Current: Version}
	if a.env.Executable != nil {
		if exe, err := a.env.Executable(); err == nil {
			rep.Executable = exe
		}
	}
	manager := selfupdate.DetectManager(rep.Executable)

	// ソースからのビルドは入れ替えない（手元の版が当てにならないので、調べもしない）
	if BuildChannel != "release" || !selfupdate.Valid(Version) {
		rep.Status = "source-build"
		rep.Commands = []string{"go install github.com/ideamans/mtb/cmd/mtb@latest"}
		if err := a.printUpgrade(rep, i18n.T(
			"この mtb はソースからのビルド（版 %s）なので、入れ替えません。\n"+
				"go install github.com/ideamans/mtb/cmd/mtb@latest か、%s の手順で入れ直してください。\n",
			"This mtb was built from source (version %s), so it will not be replaced.\n"+
				"Use go install github.com/ideamans/mtb/cmd/mtb@latest or reinstall following %s.\n"), Version, installGuideURL); err != nil {
			return err
		}
		if check {
			return nil
		}
		return errors.New(i18n.T("ソースからのビルドは mtb upgrade で入れ替えられません", "a build from source cannot be replaced by mtb upgrade"))
	}

	if a.env.NewDoer == nil {
		return sourceError(errors.New("no HTTP client"))
	}
	doer := a.env.NewDoer(false, a.g.timeout)
	ctx := context.Background()
	ua := "mtb/" + Version
	latest, err := selfupdate.Latest(ctx, doer, ua)
	if err != nil {
		return sourceError(err)
	}
	rep.Latest = latest

	switch c := selfupdate.Compare(Version, latest); {
	case c == 0:
		rep.Status = "up-to-date"
		return a.printUpgrade(rep, i18n.T("最新版です（%s）。\n", "Already the latest version (%s).\n"), Version)
	case c > 0:
		rep.Status = "newer-local"
		return a.printUpgrade(rep, i18n.T("手元（%s）のほうがリリースの最新版（%s）より新しいので、何もしません。\n",
			"This mtb (%s) is newer than the latest release (%s); nothing to do.\n"), Version, latest)
	}

	if manager != selfupdate.ManagerNone {
		rep.Status = "package-manager"
		rep.Manager = string(manager)
		rep.Commands = manager.Command()
		if err := a.printUpgrade(rep, i18n.T(
			"新しい版があります: %s → %s\nこの mtb は %s で入れたもの（%s）なので、書き換えません。次のコマンドで更新してください:\n  %s\n",
			"A newer version is available: %s -> %s\nThis mtb was installed with %s (%s), so it is left alone. Update it with:\n  %s\n"),
			Version, latest, manager, rep.Executable, strings.Join(rep.Commands, "\n  ")); err != nil {
			return err
		}
		if check {
			return nil
		}
		return fmt.Errorf(i18n.T("%s で入れた mtb は mtb upgrade では入れ替えません", "an mtb installed with %s is not replaced by mtb upgrade"), manager)
	}

	if check {
		rep.Status = "available"
		return a.printUpgrade(rep, i18n.T("新しい版があります: %s → %s（mtb upgrade で入れ替えます）\n",
			"A newer version is available: %s -> %s (run mtb upgrade to install it)\n"), Version, latest)
	}

	if rep.Executable == "" || a.env.SelfFS == nil {
		return errors.New(i18n.T("実行ファイルの場所が分かりません", "cannot find where this executable is"))
	}
	data, err := selfupdate.Fetch(ctx, doer, ua, latest, goos, goarch)
	if err != nil {
		return sourceError(err)
	}
	if err := selfupdate.Replace(a.env.SelfFS, rep.Executable, data, goos == "windows"); err != nil {
		return fmt.Errorf(i18n.T(
			"%s を入れ替えられませんでした（元のまま残っています）: %w\n書き込める権限で実行し直すか（sudo mtb upgrade など）、%s の手順で入れ直してください",
			"could not replace %s (it is unchanged): %w\nrun again with permission to write there (e.g. sudo mtb upgrade) or reinstall following %s"),
			rep.Executable, err, installGuideURL)
	}
	rep.Status = "upgraded"
	return a.printUpgrade(rep, i18n.T("%s → %s に入れ替えました（%s）。\n", "Upgraded %s -> %s (%s).\n"), Version, latest, rep.Executable)
}

// printUpgrade は --format json なら結果の JSON を、そうでなければ文を出します。
func (a *App) printUpgrade(rep upgradeReport, format string, args ...any) error {
	if a.g.format == "json" {
		buf, err := json.MarshalIndent(rep, "", "  ")
		if a.g.compact {
			buf, err = json.Marshal(rep)
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.env.Out, string(buf))
		return err
	}
	_, err := fmt.Fprintf(a.env.Out, format, args...)
	return err
}
