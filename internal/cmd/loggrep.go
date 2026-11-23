package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ideamans/mtb/internal/i18n"
)

// ログの絞り込み（mtb history と mtb logs で共通）。
//
// --grep は1行の中身（キーと値すべて）を、大文字小文字を区別せずに部分一致で探します。
// 空白で区切った語・繰り返した --grep はすべて含むもの（AND）。JSON の文字列のままだと
// < > & が \u003c のように書かれているので、読み戻した値で探します。

func grepHelp() string {
	return i18n.T(
		"中身（パス・パラメータ・作業の説明・エラーなど全部）に含まれる語で絞る。大文字小文字を区別しない。空白区切り・繰り返しはすべてを含むもの",
		"filter by words found anywhere in the entry (path, parameters, note, error and so on); case-insensitive; space-separated or repeated words must all match")
}

func countHelp() string {
	return i18n.T("該当する件数だけを出す（--limit は効かない）", "print only the number of matches (ignores --limit)")
}

// grepWords は --grep の値を小文字の語に分けます。
func grepWords(args []string) []string {
	var out []string
	for _, a := range args {
		for _, w := range strings.Fields(a) {
			out = append(out, strings.ToLower(w))
		}
	}
	return out
}

// grepMatch は1行がすべての語を含むかどうかです。語が無ければ常に真。
func grepMatch(line []byte, words []string) bool {
	if len(words) == 0 {
		return true
	}
	text := strings.ToLower(grepText(line))
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// grepText は JSON の1行を、キーと値を並べた素の文字列にします。読めなければ行そのもの。
func grepText(line []byte) string {
	var v any
	if json.Unmarshal(line, &v) != nil {
		return string(line)
	}
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				b.WriteString(k)
				b.WriteByte('\n')
				walk(t[k])
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		case nil:
		default:
			fmt.Fprint(&b, t)
			b.WriteByte('\n')
		}
	}
	walk(v)
	return b.String()
}
