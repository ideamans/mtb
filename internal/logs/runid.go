package logs

import (
	"crypto/rand"
	"time"
)

// crockford は ULID と同じ Crockford の base32 です（I L O U を使わない）。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewRunID は実行1回ごとの ID を作ります（ULID と同じ形の26文字）。
// 先頭が時刻なので、文字列の順に並べると起動した順になります。
func NewRunID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:])
	// 128 ビットを 5 ビットずつ（先頭は 3 ビット）
	out := make([]byte, 26)
	var acc uint64
	bits := 0
	pos := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && pos >= 0 {
			out[pos] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			pos--
		}
	}
	if pos >= 0 {
		out[pos] = crockford[acc&31]
	}
	return string(out)
}
