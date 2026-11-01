package main

import (
	"encoding/json"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func FuzzRelayGrant(f *testing.F) {
	for _, s := range []string{`{"data":{}}`, `{"data":{"notice_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaA","lease_token":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbA","generation":2,"server_time":100,"expires_at":60100}}`, `null`, `{"data":{"server_time":9223372036854775807,"expires_at":-9223372036854775808}}`, `{"data":{},"executable":"/canary"}`} {
		f.Add([]byte(s), true)
		f.Add([]byte(s), false)
	}

	f.Fuzz(func(t *testing.T, data []byte, claim bool) {
		g, err := decodeRelayGrant(data, claim)
		if err != nil {
			return
		}

		if len(data) > 8192 {
			t.Fatal("accepted oversized response")
		}

		if claim && g == (stack.RelayGrant{}) {
			return
		}

		if !stack.ValidID(g.NoticeID) || g.Generation < 1 || g.ServerTime < 1 || g.ExpiresAt <= g.ServerTime || g.ExpiresAt-g.ServerTime > 60000 {
			t.Fatal("unsafe grant")
		}

		if claim && !stack.ValidID(g.LeaseToken) || !claim && g.LeaseToken != "" {
			t.Fatal("wrong token shape")
		}

		canonical, _ := json.Marshal(map[string]any{"data": g})
		again, err := decodeRelayGrant(canonical, claim)
		if err != nil || again != g {
			t.Fatal("noncanonical accepted grant")
		}
	})
}
