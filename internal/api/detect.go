package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ideamans/mtb/internal/i18n"
)

// DetectVersion は、接続先が受け付ける Data API のいちばん新しい版を調べます。
//
// v{max} から v1 へ順に GET /vN/endpoints を送り、最初に通った版を返します。
// /endpoints はサインインが要らず、v1 からどの版にもあります。MT 7 以降は
// 自分より新しい版を指定されると 400（API Version is invalid）を返すので、
// そこで1つ下げます。
//
// **MT 8.0 までは新しすぎる版も断らない**（6.2.2・7.0・8.0.2 は /v7/endpoints にも
// 200 を返す。2026-11-20 に実測。400 で断るのは 8.8 以降）。その代わり一覧の各
// エンドポイントが version を持つので、通ったら「その最大値」と「頼んだ版」の
// 小さい方を採ります。これで MT 6.2 は v3、7.0 は v4、8.0 は v6 になります。
//
// それ以外の失敗（接続できない・HTML が返る・Data API が無効など）は版の
// 問題ではないので、その場でやめて理由を返します。
func (c *Client) DetectVersion(ctx context.Context, max int) (int, error) {
	var last error
	for v := max; v >= 1; v-- {
		probe := *c
		probe.APIVersion = fmt.Sprintf("v%d", v)
		probe.Tokens = &MemoryTokenStore{}
		resp, err := probe.Do(ctx, Request{Method: http.MethodGet, Path: "/endpoints", NoAuth: true})
		if err == nil {
			if newest, ok := endpointListMax(resp); ok {
				if newest > 0 && newest < v {
					return newest, nil
				}
				return v, nil
			}
			return 0, errors.New(i18n.T(
				"Data API の応答ではありません（URL が mt-data-api.cgi を指しているか確かめてください）",
				"the response is not from the Data API (check that the URL points at mt-data-api.cgi)"))
		}
		e, ok := AsError(err)
		if !ok {
			return 0, err
		}
		// 本文が MT のエラーでなければ（nginx の 404 の HTML など）、
		// それを丸ごと見せても役に立たないので、URL の取り違えだと言う。
		if e.Message == "" {
			return 0, fmt.Errorf(i18n.T(
				"HTTP %d が返り、Data API の応答ではありません（URL が mt-data-api.cgi を指しているか確かめてください）",
				"got HTTP %d, which is not a Data API response (check that the URL points at mt-data-api.cgi)"), e.Status)
		}
		// 新しすぎる版は 400、古い MT が経路を知らない版は 404 で返ることがある。
		if e.Status != http.StatusBadRequest && !(e.UnknownEndpoint() && e.Message != "") {
			return 0, err
		}
		last = err
	}
	return 0, fmt.Errorf(i18n.T(
		"どの版（v1〜v%d）でも応答がありません: %w",
		"no version (v1 to v%d) answered: %w"), max, last)
}

// endpointListMax は /endpoints の応答から、エンドポイントの version の最大値を
// 返します。2つ目の値は「/endpoints の応答として読めたか」です。version は
// 数値でも文字列でも来る（MT 6 は文字列）ので、どちらも読みます。
func endpointListMax(resp *Response) (int, bool) {
	if resp == nil || !resp.IsJSON() {
		return 0, false
	}
	var body struct {
		Items []struct {
			Version flexInt `json:"version"`
		} `json:"items"`
	}
	if json.Unmarshal(resp.Body, &body) != nil || body.Items == nil {
		return 0, false
	}
	newest := 0
	for _, it := range body.Items {
		if int(it.Version) > newest {
			newest = int(it.Version)
		}
	}
	return newest, true
}
