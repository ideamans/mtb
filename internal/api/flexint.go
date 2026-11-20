package api

import (
	"encoding/json"
	"strconv"
	"strings"
)

// flexInt は、数値でも文字列でも来る整数です。MT 6 は expiresIn や
// エンドポイントの version を文字列で返し、MT 7 以降は数値で返します。
// 読めない値（空文字など）は 0 にします。
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt(n)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		*f = 0
		return nil
	}
	*f = flexInt(n)
	return nil
}
