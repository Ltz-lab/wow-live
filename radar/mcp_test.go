package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

func rpc(t *testing.T, h http.Handler, body string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)))
	var out map[string]any
	if rr.Body.Len() > 0 {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v", rr.Body.String(), err)
		}
	}
	return rr.Code, out
}

func TestMCP(t *testing.T) {
	w := newWorld(1)
	me, rabbit, sten := codec.GUID{Lo: 3}, codec.GUID{Hi: 1, Lo: 1}, codec.GUID{Hi: 1, Lo: 2}
	w.self = me
	w.ents[me] = &ent{GUID: "me", Kind: "player", X: 0, Y: 0}
	w.ents[rabbit] = &ent{GUID: "r", Kind: "unit", Entry: 721, X: 3, Y: 4}
	w.ents[sten] = &ent{GUID: "s", Kind: "unit", Entry: 658, X: 300, Y: 0}
	w.names[721], w.names[658] = "Rabbit", "Sten Stoutarm"
	h := mcpHandler(w, 400)

	if code, _ := rpc(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("notification: %d", code)
	}
	_, init := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if v := init["result"].(map[string]any)["protocolVersion"]; v != "2025-03-26" {
		t.Fatalf("protocol %v", v)
	}
	_, list := rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if n := len(list["result"].(map[string]any)["tools"].([]any)); n != len(mcpTools) {
		t.Fatalf("%d tools", n)
	}

	text := func(name, args string) string {
		_, r := rpc(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
		return r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	if s := text("radar_around", `{"radius":50}`); !strings.Contains(s, `"Rabbit"`) || !strings.Contains(s, `"dist": 5`) || strings.Contains(s, "Sten") {
		t.Fatalf("around: %s", s)
	}
	if s := text("radar_find", `{"name":"STEN"}`); !strings.Contains(s, `"dist": 300`) {
		t.Fatalf("find: %s", s)
	}
	_, bad := rpc(t, h, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`)
	if bad["result"].(map[string]any)["isError"] != true {
		t.Fatalf("unknown tool: %v", bad)
	}
}
