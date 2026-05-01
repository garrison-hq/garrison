package mempalace

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeQueryExec is a parallel of fakeClientExec scoped to QueryClient
// tests so the two suites stay independent. Records argv + stdin and
// returns canned stdout / stderr / err.
type fakeQueryExec struct {
	stdout, stderr []byte
	err            error

	calls  [][]string
	stdins []string
}

func (f *fakeQueryExec) Run(_ context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdins = append(f.stdins, string(b))
	} else {
		f.stdins = append(f.stdins, "")
	}
	return f.stdout, f.stderr, f.err
}

func (f *fakeQueryExec) RunStream(
	_ context.Context,
	_ []string,
	_ func(stdin io.WriteCloser) error,
	_ func(stdout io.Reader) error,
) (*exec.Cmd, error) {
	return nil, errors.New("fakeQueryExec: RunStream not implemented")
}

// initOK wraps the standard initialize-response (id=1) the MCP server
// emits before any tools/call response. Used to compose canned stdouts.
const initOK = `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"

// drawerToolCallResponse wraps a tools/call result (id=2) carrying the
// given JSON `text` payload — mirrors the MemPalace MCP server's
// content[0].text shape.
func drawerToolCallResponse(text string) string {
	// JSON-encode the text payload to escape it inside the "text" string.
	// We build the wrapper literally to keep the fixture readable.
	escaped := strings.ReplaceAll(text, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, "\n", `\n`)
	return `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"` + escaped + `"}]}}` + "\n"
}

func newQueryClientWithFake(fake *fakeQueryExec) *QueryClient {
	return &QueryClient{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Exec:               fake,
	}
}

// TestRecentDrawers_ParsesSidecarResponse — fake exec returns canonical
// list_drawers JSON; QueryClient returns []DrawerEntry with the right
// fields, sorted DESC by WrittenAt, truncated to limit.
func TestRecentDrawers_ParsesSidecarResponse(t *testing.T) {
	payload := `{"drawers":[
        {"id":"d1","drawer_name":"first","wing_name":"wing_planner","room_name":"hall_events","written_at":"2026-04-30T10:00:00Z","body_preview":"body one","source_agent_role_slug":"planner"},
        {"id":"d2","drawer_name":"second","wing_name":"wing_executor","room_name":"hall_events","written_at":"2026-04-30T12:00:00Z","body_preview":"body two"},
        {"id":"d3","drawer_name":"third","wing_name":"wing_planner","room_name":"hall_events","written_at":"2026-04-30T11:00:00Z","body_preview":"body three"}
    ]}`
	fake := &fakeQueryExec{
		stdout: []byte(initOK + drawerToolCallResponse(payload)),
	}
	c := newQueryClientWithFake(fake)

	got, err := c.RecentDrawers(context.Background(), 50)
	if err != nil {
		t.Fatalf("RecentDrawers err: %v", err)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("calls=%d; want 1", len(fake.calls))
	}
	wantArgs := []string{
		"exec", "-i", "garrison-mempalace",
		"python", "-m", "mempalace.mcp_server",
		"--palace", "/palace",
	}
	for i, w := range wantArgs {
		if fake.calls[0][i] != w {
			t.Errorf("argv[%d]=%q; want %q", i, fake.calls[0][i], w)
		}
	}
	if !strings.Contains(fake.stdins[0], `"name":"mempalace_list_drawers"`) {
		t.Errorf("stdin missing list_drawers tool_call: %s", fake.stdins[0])
	}

	if len(got) != 3 {
		t.Fatalf("len(got)=%d; want 3", len(got))
	}
	// DESC by written_at: d2 (12:00) → d3 (11:00) → d1 (10:00).
	wantOrder := []string{"d2", "d3", "d1"}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("got[%d].ID=%q; want %q", i, got[i].ID, want)
		}
	}
	if got[0].WingName != "wing_executor" {
		t.Errorf("got[0].WingName=%q; want wing_executor", got[0].WingName)
	}
	if got[2].SourceAgentRoleSlug != "planner" {
		t.Errorf("got[2].SourceAgentRoleSlug=%q; want planner", got[2].SourceAgentRoleSlug)
	}
}

// TestRecentDrawers_TruncatesBodyTo200Chars — the supervisor truncates
// body_preview to ≤200 runes (UTF-8-safe; never splits a multi-byte
// rune).
func TestRecentDrawers_TruncatesBodyTo200Chars(t *testing.T) {
	// 250 ASCII chars → must truncate to 200.
	longASCII := strings.Repeat("a", 250)
	// 250 multi-byte runes (each "é" is 2 bytes in UTF-8) → must
	// truncate to 200 RUNES (400 bytes), not 200 bytes.
	longMultibyte := strings.Repeat("é", 250)

	payload := `{"drawers":[
        {"id":"d1","wing_name":"w","room_name":"r","written_at":"2026-04-30T10:00:00Z","body_preview":"` + longASCII + `"},
        {"id":"d2","wing_name":"w","room_name":"r","written_at":"2026-04-30T11:00:00Z","body_preview":"` + longMultibyte + `"}
    ]}`
	fake := &fakeQueryExec{stdout: []byte(initOK + drawerToolCallResponse(payload))}
	c := newQueryClientWithFake(fake)

	got, err := c.RecentDrawers(context.Background(), 50)
	if err != nil {
		t.Fatalf("RecentDrawers err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d; want 2", len(got))
	}
	for _, d := range got {
		runes := []rune(d.BodyPreview)
		if len(runes) > BodyPreviewMaxRunes {
			t.Errorf("preview=%d runes; want ≤%d", len(runes), BodyPreviewMaxRunes)
		}
	}
	// Multi-byte case: must not split a rune (string must remain valid UTF-8).
	for _, d := range got {
		if !isValidUTF8(d.BodyPreview) {
			t.Errorf("preview is not valid UTF-8: %q", d.BodyPreview)
		}
	}
}

// TestRecentDrawers_PropagatesSidecarError — fake returns an MCP error
// response; QueryClient surfaces ErrSidecarUnreachable.
func TestRecentDrawers_PropagatesSidecarError(t *testing.T) {
	fake := &fakeQueryExec{
		stdout: []byte(initOK + `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"palace boom"}}` + "\n"),
	}
	c := newQueryClientWithFake(fake)
	_, err := c.RecentDrawers(context.Background(), 50)
	if !errors.Is(err, ErrSidecarUnreachable) {
		t.Fatalf("err=%v; want ErrSidecarUnreachable", err)
	}
}

// TestRecentDrawers_PropagatesNetworkError — fake returns a docker-exec
// error (e.g. container unreachable); QueryClient surfaces
// ErrSidecarUnreachable.
func TestRecentDrawers_PropagatesNetworkError(t *testing.T) {
	fake := &fakeQueryExec{
		stderr: []byte("Error: No such container: garrison-mempalace"),
		err:    errors.New("exit status 1"),
	}
	c := newQueryClientWithFake(fake)
	_, err := c.RecentDrawers(context.Background(), 50)
	if !errors.Is(err, ErrSidecarUnreachable) {
		t.Fatalf("err=%v; want ErrSidecarUnreachable", err)
	}
	if !strings.Contains(err.Error(), "docker exec") {
		t.Errorf("err msg should mention docker exec: %v", err)
	}
}

// TestRecentKGTriples_ParsesSidecarResponse — fake exec returns canonical
// kg_timeline JSON (using the `facts` wrapper key per the M2.2 live-run
// finding); QueryClient returns []KGTriple sorted DESC.
func TestRecentKGTriples_ParsesSidecarResponse(t *testing.T) {
	srcTicket := "ticket_42"
	payload := `{"facts":[
        {"id":"t1","subject":"agent_planner","predicate":"completed","object":"ticket_41","written_at":"2026-04-30T09:00:00Z","source_ticket_id":"ticket_41"},
        {"id":"t2","subject":"agent_executor","predicate":"created","object":"file.go","written_at":"2026-04-30T11:00:00Z","source_ticket_id":"` + srcTicket + `","source_agent_role_slug":"executor"},
        {"id":"t3","subject":"decision_x","predicate":"because","object":"reason_y","written_at":"2026-04-30T10:00:00Z"}
    ]}`
	fake := &fakeQueryExec{stdout: []byte(initOK + drawerToolCallResponse(payload))}
	c := newQueryClientWithFake(fake)

	got, err := c.RecentKGTriples(context.Background(), 50)
	if err != nil {
		t.Fatalf("RecentKGTriples err: %v", err)
	}
	if !strings.Contains(fake.stdins[0], `"name":"mempalace_kg_timeline"`) {
		t.Errorf("stdin missing kg_timeline tool_call: %s", fake.stdins[0])
	}
	if len(got) != 3 {
		t.Fatalf("len=%d; want 3", len(got))
	}
	// DESC: t2 (11:00) → t3 (10:00) → t1 (09:00).
	wantOrder := []string{"t2", "t3", "t1"}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("got[%d].ID=%q; want %q", i, got[i].ID, want)
		}
	}
	if got[0].SourceTicketID == nil || *got[0].SourceTicketID != srcTicket {
		t.Errorf("got[0].SourceTicketID=%v; want pointer to %q", got[0].SourceTicketID, srcTicket)
	}
	if got[0].SourceAgentRoleSlug == nil || *got[0].SourceAgentRoleSlug != "executor" {
		t.Errorf("got[0].SourceAgentRoleSlug=%v; want pointer to executor", got[0].SourceAgentRoleSlug)
	}
}

// TestRecentKGTriples_HandlesOptionalSourceFields — a triple with no
// source_ticket_id / source_agent_role_slug fields decodes with nil
// pointers (per the FR-681 contract).
func TestRecentKGTriples_HandlesOptionalSourceFields(t *testing.T) {
	payload := `{"triples":[
        {"id":"t1","subject":"a","predicate":"b","object":"c","written_at":"2026-04-30T10:00:00Z"}
    ]}`
	fake := &fakeQueryExec{stdout: []byte(initOK + drawerToolCallResponse(payload))}
	c := newQueryClientWithFake(fake)

	got, err := c.RecentKGTriples(context.Background(), 50)
	if err != nil {
		t.Fatalf("RecentKGTriples err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d; want 1", len(got))
	}
	if got[0].SourceTicketID != nil {
		t.Errorf("SourceTicketID=%v; want nil pointer", got[0].SourceTicketID)
	}
	if got[0].SourceAgentRoleSlug != nil {
		t.Errorf("SourceAgentRoleSlug=%v; want nil pointer", got[0].SourceAgentRoleSlug)
	}
}

// isValidUTF8 reports whether the string is a sequence of valid UTF-8
// runes (no half-truncated multi-byte sequences).
func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD && len(s) > 0 {
			// rune-iteration substitutes 0xFFFD for invalid bytes — flag.
			// (Permissive: only treats explicit 0xFFFD in the source as a
			// genuine character, but our fixtures don't use that codepoint.)
			return false
		}
	}
	return true
}
