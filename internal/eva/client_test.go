package eva

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEndpoint(t *testing.T) {
	c := New("https://eva.staff.rfn.ru/api", "", "", "", "")
	for _, m := range []string{"CmfTask.list", "CmfComment.list"} {
		got := c.endpoint(m)
		want := "https://eva.staff.rfn.ru/api/?m=" + m
		if got != want {
			t.Errorf("endpoint(%s) = %q, want %q", m, got, want)
		}
	}
}

func TestRPCBodyMarshal(t *testing.T) {
	req := rpcRequest{
		JSONRPC: "2.2",
		Method:  "CmfTask.list",
		CallID:  "3f3df7bb-ce42-469c-b35f-1589f0da276d",
		Kwargs: map[string]any{
			"filter": []any{[]any{"status", "!=", "closed"}},
			"slice":  []any{0, 100},
		},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"jsonrpc":"2.2"`) ||
		!strings.Contains(s, `"method":"CmfTask.list"`) ||
		!strings.Contains(s, `"callid":"3f3df7bb-ce42-469c-b35f-1589f0da276d"`) ||
		!strings.Contains(s, `["status","!=","closed"]`) ||
		!strings.Contains(s, `[0,100]`) {
		t.Errorf("unexpected body: %s", s)
	}
}

func TestTokenAuthHeader(t *testing.T) {
	c := NewWithAPIToken("https://x/api", "sekret", "Authorization", "Bearer")
	if !strings.HasPrefix(c.authHeader, "Authorization: Bearer sekret") {
		t.Errorf("authHeader = %q", c.authHeader)
	}
}
