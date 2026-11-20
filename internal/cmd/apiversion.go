package cmd

import (
	"strings"

	"github.com/ideamans/mtb/internal/spec"
)

// 手書きの機能（dump・templates pull/push・--replace・--verify-publish）は、
// 生成コマンドと違って spec の操作を経由せずにパスを組み立てます。版の
// 確かめが抜けないよう、使うパスを埋め込みの定義で引いて同じ判断をします。

// specPath は "/sites/%s/templates" や "/sites/1/templates" を
// 定義の形（"/sites/{site_id}/templates"）にします。サイトの ID しか
// 入らない場所でだけ使います。
func specPath(p string) string {
	p = strings.ReplaceAll(p, "%s", "{site_id}")
	if rest, ok := strings.CutPrefix(p, "/sites/"); ok {
		if id, tail, _ := strings.Cut(rest, "/"); id != "{site_id}" && isInteger(id) {
			p = "/sites/{site_id}"
			if tail != "" {
				p += "/" + tail
			}
		}
	}
	return p
}

// lookupOp は手書きの機能が使う操作を定義から引きます。定義に無いもの
// （プラグイン由来など）は nil で、版では止めません。
func (a *App) lookupOp(method, path string) *spec.Operation {
	if a.env.Spec == nil {
		return nil
	}
	return a.env.Spec.Find(method, specPath(path))
}

// apiAvailable は、接続先の版でその操作が使えるかです。
func (a *App) apiAvailable(method, path string) bool {
	op := a.lookupOp(method, path)
	if op == nil {
		return true
	}
	r, err := a.resolve()
	if err != nil {
		return true
	}
	return checkAvailable(op, r.APIVersion) == nil
}

// requireAPI は、接続先の版でその操作が使えなければ理由つきのエラーを返します。
func (a *App) requireAPI(method, path string) error {
	op := a.lookupOp(method, path)
	if op == nil {
		return nil
	}
	r, err := a.resolve()
	if err != nil {
		return err
	}
	return checkAvailable(op, r.APIVersion)
}
