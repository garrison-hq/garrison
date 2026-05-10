package garrisonmutate

import (
	"encoding/json"
	"strings"
	"testing"
)

// Unit-level tests for parseRegisterMcpServerArgs — exercises the
// validation surface without a live DB. The DB-backed paths live in
// register_mcp_server_test.go (//go:build integration).

func TestParseRegister_RejectsEmptyCustomerSlug(t *testing.T) {
	_, res := parseRegisterMcpServerArgs(json.RawMessage(`{"name":"x.foo","transport":"http","url":"https://y"}`))
	if res == nil {
		t.Fatal("expected rejection on empty customer_slug")
	}
	if !strings.Contains(res.Message, "customer_slug is required") {
		t.Errorf("unexpected message: %s", res.Message)
	}
}

func TestParseRegister_RejectsEmptyName(t *testing.T) {
	_, res := parseRegisterMcpServerArgs(json.RawMessage(`{"customer_slug":"x","transport":"http","url":"https://y"}`))
	if res == nil || !strings.Contains(res.Message, "name is required") {
		t.Errorf("expected name-required rejection; got %+v", res)
	}
}

func TestParseRegister_RejectsBadTransport(t *testing.T) {
	_, res := parseRegisterMcpServerArgs(json.RawMessage(`{"customer_slug":"x","name":"x.foo","transport":"grpc"}`))
	if res == nil || !strings.Contains(res.Message, "transport") {
		t.Errorf("expected transport rejection; got %+v", res)
	}
}

func TestParseRegister_AcceptsValidTransports(t *testing.T) {
	for _, transport := range []string{"http", "stdio", "sse"} {
		body := `{"customer_slug":"garrison","name":"garrison.x","transport":"` + transport + `"`
		if transport != "stdio" {
			body += `,"url":"https://y"`
		}
		body += `}`
		args, res := parseRegisterMcpServerArgs(json.RawMessage(body))
		if res != nil {
			t.Errorf("transport=%s rejected: %+v", transport, res)
			continue
		}
		if args.Transport != transport {
			t.Errorf("transport=%s not preserved", transport)
		}
	}
}

func TestParseRegister_RejectsHTTPWithoutURL(t *testing.T) {
	_, res := parseRegisterMcpServerArgs(json.RawMessage(`{"customer_slug":"garrison","name":"garrison.x","transport":"http"}`))
	if res == nil || !strings.Contains(res.Message, "url is required") {
		t.Errorf("expected url-required rejection for http; got %+v", res)
	}
}

func TestParseRegister_RejectsCustomerPrefixViolation(t *testing.T) {
	_, res := parseRegisterMcpServerArgs(json.RawMessage(`{"customer_slug":"garrison","name":"linear","transport":"http","url":"https://y"}`))
	if res == nil || !strings.Contains(res.Message, "customer-prefix") {
		t.Errorf("expected customer-prefix rejection; got %+v", res)
	}
}

func TestParseRegister_AcceptsCustomerPrefix(t *testing.T) {
	_, res := parseRegisterMcpServerArgs(json.RawMessage(`{"customer_slug":"garrison","name":"garrison.linear","transport":"http","url":"https://y"}`))
	if res != nil {
		t.Errorf("expected acceptance; got %+v", res)
	}
}

func TestParseRegister_RejectsMalformedJSON(t *testing.T) {
	_, res := parseRegisterMcpServerArgs(json.RawMessage(`{not json`))
	if res == nil || !strings.Contains(res.Message, "parse args") {
		t.Errorf("expected parse-args rejection; got %+v", res)
	}
}

func TestFindServerActionVerb(t *testing.T) {
	if v := FindServerActionVerb("register_mcp_server"); v == nil {
		t.Error("register_mcp_server missing from ServerActionVerbs registry")
	}
	if v := FindServerActionVerb("create_ticket"); v != nil {
		t.Error("create_ticket should NOT be in ServerActionVerbs (chat-side only)")
	}
}

func TestServerActionVerbsShape(t *testing.T) {
	if len(ServerActionVerbs) == 0 {
		t.Fatal("ServerActionVerbs empty")
	}
	v := ServerActionVerbs[0]
	if v.Name != "register_mcp_server" {
		t.Errorf("first verb = %s; want register_mcp_server", v.Name)
	}
	if v.AffectedResourceType != "mcp_server" {
		t.Errorf("AffectedResourceType = %s; want mcp_server", v.AffectedResourceType)
	}
	if v.ReversibilityClass != 2 {
		t.Errorf("ReversibilityClass = %d; want 2", v.ReversibilityClass)
	}
}
