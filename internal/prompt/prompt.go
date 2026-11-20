// Package prompt は対話入力を抽象化します。
//
// 端末は外部依存の中でもテストしづらい部類なので、Prompter インタフェースに
// 閉じ込めてあります。テストは Scripted に答えを積んで渡します。
package prompt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"github.com/ideamans/mtb/internal/i18n"
)

// ErrNotInteractive は対話できない場面で入力を求められたときのエラーです。
// 「エージェントが実行して黙って止まる」を避けるため、フラグや環境変数で
// 与えるべきだと分かる文言にしてあります。
var ErrNotInteractive = i18n.Error("対話入力ができません", "cannot read interactive input")

// Prompter は対話入力です。
type Prompter interface {
	// Line は1行を読みます。def は空入力のときの既定値です。
	Line(label, def string) (string, error)
	// Secret はエコーせずに1行を読みます。
	Secret(label string) (string, error)
	// Confirm は yes/no を読みます。
	Confirm(label string, def bool) (bool, error)
	// Select は選択肢から1つを選ばせます。def は最初に選ばれている値です。
	Select(label string, options []Option, def string) (string, error)
	// Interactive は対話できる状況かを返します。
	Interactive() bool
}

// Option は Select の選択肢1つです。
type Option struct {
	// Value は選んだときに返る値です。
	Value string
	// Label は画面に出す文言です。
	Label string
}

// ErrAborted は入力の途中で Ctrl+C などで抜けたときのエラーです。
var ErrAborted = i18n.Error("中止しました", "aborted")

// Terminal は実際の端末から読む実装です。
//
// 入力欄は charmbracelet/huh で描きます。素の1行読みでは矢印キーが
// エスケープ列（^[[D）のまま入り、既定値を少し直すこともできなかったためです。
// huh なら行の編集・既定値の書き換え・選択肢からの選択ができます。
type Terminal struct {
	In  io.Reader
	Out io.Writer
	// Fd は端末判定に使うファイルディスクリプタです。
	Fd int
}

// NewTerminal は標準入出力を使う Prompter を作ります。
func NewTerminal(in io.Reader, out io.Writer) *Terminal {
	fd := -1
	if f, ok := in.(*os.File); ok {
		fd = int(f.Fd())
	}
	return &Terminal{In: in, Out: out, Fd: fd}
}

// Interactive は入力が端末かどうかです。
func (t *Terminal) Interactive() bool { return t.Fd >= 0 && term.IsTerminal(t.Fd) }

// run は1項目だけのフォームを動かします。
func (t *Terminal) run(label string, field huh.Field) error {
	if !t.Interactive() {
		return fmt.Errorf("%w: %s", ErrNotInteractive, label)
	}
	err := huh.NewForm(huh.NewGroup(field)).
		WithInput(t.In).
		WithOutput(t.Out).
		WithTheme(huh.ThemeBase()).
		Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrAborted
	}
	return err
}

// echo は答えを1行残します。huh は確定したフォームを画面から消すので、
// 残さないと「どの URL で登録したか・どの版を選んだか」が後から見えません。
func (t *Terminal) echo(label, value string) {
	fmt.Fprintf(t.Out, "✔ %s: %s\n", label, value)
}

// Line は1行を読みます。既定値は入力欄に最初から入っていて、そのまま
// 確定することも、書き換えることもできます。
func (t *Terminal) Line(label, def string) (string, error) {
	value := def
	if err := t.run(label, huh.NewInput().Title(label).Value(&value)); err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = def
	}
	t.echo(label, value)
	return value, nil
}

// Secret はエコーせずに1行を読みます。
func (t *Terminal) Secret(label string) (string, error) {
	var value string
	if err := t.run(label, huh.NewInput().Title(label).EchoMode(huh.EchoModePassword).Value(&value)); err != nil {
		return "", err
	}
	t.echo(label, "********")
	return strings.TrimSpace(value), nil
}

// Confirm は yes/no を読みます。
func (t *Terminal) Confirm(label string, def bool) (bool, error) {
	value := def
	field := huh.NewConfirm().Title(label).
		Affirmative(i18n.T("はい", "Yes")).
		Negative(i18n.T("いいえ", "No")).
		Value(&value)
	if err := t.run(label, field); err != nil {
		return false, err
	}
	answer := i18n.T("いいえ", "No")
	if value {
		answer = i18n.T("はい", "Yes")
	}
	t.echo(label, answer)
	return value, nil
}

// Select は選択肢から1つを選ばせます。def の選択肢にカーソルを置いて始めます。
func (t *Terminal) Select(label string, options []Option, def string) (string, error) {
	value := def
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o.Label, o.Value)
	}
	if err := t.run(label, huh.NewSelect[string]().Title(label).Options(opts...).Value(&value)); err != nil {
		return "", err
	}
	shown := value
	for _, o := range options {
		if o.Value == value {
			shown = o.Label
		}
	}
	t.echo(label, shown)
	return value, nil
}

// Scripted は答えをあらかじめ積んでおく実装です。テスト用です。
type Scripted struct {
	// Lines は Line が順に返す答えです。
	Lines []string
	// Secrets は Secret が順に返す答えです。
	Secrets []string
	// Confirms は Confirm が順に返す答えです。
	Confirms []bool
	// Selects は Select が順に返す答えです。尽きたら既定値を返します。
	Selects []string
	// NotInteractive を立てると Interactive が false になり、入力要求は
	// ErrNotInteractive になります。
	NotInteractive bool
	// Asked は聞かれたラベルの記録です。
	Asked []string
}

// Interactive は対話できるかを返します。
func (s *Scripted) Interactive() bool { return !s.NotInteractive }

// Line は積んである答えを順に返します。
func (s *Scripted) Line(label, def string) (string, error) {
	s.Asked = append(s.Asked, label)
	if s.NotInteractive {
		return "", fmt.Errorf("%w: %s", ErrNotInteractive, label)
	}
	if len(s.Lines) == 0 {
		return def, nil
	}
	answer := s.Lines[0]
	s.Lines = s.Lines[1:]
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

// Secret は積んである答えを順に返します。
func (s *Scripted) Secret(label string) (string, error) {
	s.Asked = append(s.Asked, label)
	if s.NotInteractive {
		return "", fmt.Errorf("%w: %s", ErrNotInteractive, label)
	}
	if len(s.Secrets) == 0 {
		return "", nil
	}
	answer := s.Secrets[0]
	s.Secrets = s.Secrets[1:]
	return answer, nil
}

// Confirm は積んである答えを順に返します。
func (s *Scripted) Confirm(label string, def bool) (bool, error) {
	s.Asked = append(s.Asked, label)
	if s.NotInteractive {
		return false, fmt.Errorf("%w: %s", ErrNotInteractive, label)
	}
	if len(s.Confirms) == 0 {
		return def, nil
	}
	answer := s.Confirms[0]
	s.Confirms = s.Confirms[1:]
	return answer, nil
}

// Select は積んである答えを順に返します。選択肢に無い答えはエラーにします
// （本物の端末では選べないので、テストの書き間違いです）。
func (s *Scripted) Select(label string, options []Option, def string) (string, error) {
	s.Asked = append(s.Asked, label)
	if s.NotInteractive {
		return "", fmt.Errorf("%w: %s", ErrNotInteractive, label)
	}
	if len(s.Selects) == 0 {
		return def, nil
	}
	answer := s.Selects[0]
	s.Selects = s.Selects[1:]
	for _, o := range options {
		if o.Value == answer {
			return answer, nil
		}
	}
	return "", fmt.Errorf("prompt.Scripted: %q is not among the options", answer)
}
