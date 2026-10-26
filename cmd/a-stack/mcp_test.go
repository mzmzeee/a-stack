package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func mcpInit() map[string]any {
	return map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "1"}}
}
func mcpModernMeta() map[string]any {
	return map[string]any{mcpMetaPrefix + "protocolVersion": mcpVersion, mcpMetaPrefix + "clientCapabilities": map[string]any{}}
}

type mcpReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *mcpRPCError    `json:"error"`
}

func TestMCPProtocol(t *testing.T) {
	var input bytes.Buffer
	enc := json.NewEncoder(&input)
	send := func(id any, method string, params any) {
		t.Helper()
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
	}
	input.WriteString("not json\n[]\n")
	send(nil, "ping", map[string]any{})
	send(1.5, "ping", map[string]any{})
	send(1, "tools/list", map[string]any{})
	send(2, "initialize", mcpInit())
	input.WriteString(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	// A tool notification cannot execute a write or get a response.
	input.WriteString(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"task_create"}}` + "\n")
	send("tools", "tools/list", map[string]any{})
	send(3, "tools/call", map[string]any{"name": "storage_backup"})
	send(4, "tools/call", map[string]any{"name": "task_show", "arguments": []string{}})
	send(5, "unknown", map[string]any{})
	send(6, "ping", map[string]any{})
	send(7, "tools/list", []string{})
	send(8, "initialize", mcpInit())
	send(9, "tools/list", map[string]any{"cursor": "no-such-page"})
	var out bytes.Buffer
	if err := serveMCP(context.Background(), nil, stack.Context{}, &input, &out); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(&out)
	want := []int{-32700, -32600, -32600, -32600, -32600, 0, 0, -32602, -32602, -32601, 0, -32602, -32602, -32602}
	for i, code := range want {
		if !scanner.Scan() {
			t.Fatalf("missing response %d", i)
		}
		var reply mcpReply
		if json.Unmarshal(scanner.Bytes(), &reply) != nil {
			t.Fatal("non-JSON stdout")
		}
		if code == 0 && reply.Error != nil || code != 0 && (reply.Error == nil || reply.Error.Code != code) {
			t.Fatalf("response %d: %s", i, scanner.Text())
		}
		if i == 6 {
			var result struct{ Tools []mcpTool }
			if string(reply.ID) != `"tools"` || json.Unmarshal(reply.Result, &result) != nil || len(result.Tools) != 15 {
				t.Fatal("tools/list shape or ID")
			}
			for _, tool := range result.Tools {
				if tool.InputSchema["type"] != "object" || tool.InputSchema["additionalProperties"] != false {
					t.Fatal("invalid input schema")
				}
			}
		}
	}
	if scanner.Scan() || scanner.Err() != nil {
		t.Fatal("notification response or invalid framing")
	}
}

func TestMCPModernAndVersionNegotiation(t *testing.T) {
	requests := []struct {
		method string
		params map[string]any
		code   int
	}{
		{"server/discover", map[string]any{"_meta": mcpModernMeta()}, 0},
		{"tools/list", map[string]any{"_meta": mcpModernMeta()}, 0},
		{"ping", map[string]any{"_meta": mcpModernMeta()}, 0},
		{"server/discover", map[string]any{}, -32602},
		{"tools/list", map[string]any{"_meta": map[string]any{mcpMetaPrefix + "protocolVersion": "2099-01-01"}}, -32022},
		{"tools/list", map[string]any{"_meta": map[string]any{mcpMetaPrefix + "protocolVersion": mcpVersion}}, -32602},
		{"tools/list", map[string]any{"_meta": map[string]any{mcpMetaPrefix + "protocolVersion": 42}}, -32602},
	}
	var input, output bytes.Buffer
	for i, r := range requests {
		_ = json.NewEncoder(&input).Encode(map[string]any{"jsonrpc": "2.0", "id": i, "method": r.method, "params": r.params})
	}
	if err := serveMCP(context.Background(), nil, stack.Context{}, &input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	for _, r := range requests {
		var reply mcpReply
		if decoder.Decode(&reply) != nil {
			t.Fatal("missing modern response")
		}
		if r.code != 0 {
			if reply.Error == nil || reply.Error.Code != r.code {
				t.Fatalf("wrong modern error: %+v", reply.Error)
			}
			continue
		}
		var result map[string]any
		if json.Unmarshal(reply.Result, &result) != nil || result["resultType"] != "complete" || result["_meta"] == nil {
			t.Fatal("modern result shape")
		}
	}
	for _, protocol := range append(mcpVersions[1:], "unknown") {
		var input, output bytes.Buffer
		params := mcpInit()
		params["protocolVersion"] = protocol
		_ = json.NewEncoder(&input).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": params})
		if err := serveMCP(context.Background(), nil, stack.Context{}, &input, &output); err != nil {
			t.Fatal(err)
		}
		want := protocol
		if protocol == "unknown" {
			want = "2025-11-25"
		}
		if !strings.Contains(output.String(), `"protocolVersion":"`+want+`"`) {
			t.Fatal("legacy version negotiation")
		}
	}
}

func TestMCPInputLimitsAndArguments(t *testing.T) {
	var output bytes.Buffer
	if err := serveMCP(context.Background(), nil, stack.Context{}, strings.NewReader(strings.Repeat("x", 2*stack.MaxRequestBytes)), &output); err == nil {
		t.Fatal("unbounded input")
	}
	tool := mcpTools[2] // task_show
	for _, raw := range []string{`null`, `[]`, `{}`, `{"task_id":null}`, `{"task_id":4}`, `{"task_id":"id","credential_file":"private"}`, `{"task_id":"id","read_revision":1.5}`} {
		if _, err := mcpArguments(tool, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid arguments %s", raw)
		}
	}
	for _, test := range []struct {
		tool int
		raw  string
	}{{3, `{"operation_id":"test-create","title":"Task","assign":[null]}`}, {9, `{"cursors":{"task":null}}`}} {
		if _, err := mcpArguments(mcpTools[test.tool], json.RawMessage(test.raw)); err == nil {
			t.Fatal("accepted a null collection member")
		}
	}
}

type mcpClientTest struct {
	t      *testing.T
	in     io.WriteCloser
	out    *json.Decoder
	seq    int
	modern bool
}

func newMCPClientTest(t *testing.T, f *team, credential string, modern bool) *mcpClientTest {
	t.Helper()
	command := child(t, "mcp", "--state-dir", f.state, "--credential-file", credential)
	in, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(30*time.Second, func() { _ = command.Process.Kill() })
	t.Cleanup(func() {
		_ = in.Close()
		err := command.Wait()
		timer.Stop()
		if err != nil || stderr.Len() != 0 {
			t.Errorf("MCP subprocess: %v, stderr: %s", err, stderr.String())
		}
	})
	c := &mcpClientTest{t: t, in: in, out: json.NewDecoder(out), modern: modern}
	if modern {
		c.rpc("server/discover", map[string]any{})
	} else {
		c.rpc("initialize", mcpInit())
		_ = json.NewEncoder(in).Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	}
	return c
}

func (c *mcpClientTest) rpc(method string, params map[string]any) json.RawMessage {
	c.t.Helper()
	c.seq++
	if c.modern {
		params["_meta"] = mcpModernMeta()
	}
	if err := json.NewEncoder(c.in).Encode(map[string]any{"jsonrpc": "2.0", "id": c.seq, "method": method, "params": params}); err != nil {
		c.t.Fatal(err)
	}
	var reply mcpReply
	if err := c.out.Decode(&reply); err != nil {
		c.t.Fatal(err)
	}
	var id int
	if json.Unmarshal(reply.ID, &id) != nil || id != c.seq || reply.Error != nil {
		c.t.Fatalf("unexpected RPC reply: %+v", reply)
	}
	return reply.Result
}

func (c *mcpClientTest) tool(name string, args map[string]any, wantError string) json.RawMessage {
	c.t.Helper()
	result := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	var reply struct {
		Content    []struct{ Type, Text string }
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if json.Unmarshal(result, &reply) != nil || len(reply.Content) != 1 || reply.Content[0].Type != "text" || reply.IsError != (wantError != "") || !bytes.Equal(bytes.TrimSpace(reply.Structured), bytes.TrimSpace([]byte(reply.Content[0].Text))) {
		c.t.Fatalf("invalid tool result: %s", result)
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error *stack.Error    `json:"error"`
	}
	if json.Unmarshal(reply.Structured, &envelope) != nil {
		c.t.Fatal("invalid service envelope")
	}
	if wantError != "" {
		if envelope.Error == nil || envelope.Error.Code != wantError {
			c.t.Fatalf("wanted %s: %s", wantError, reply.Structured)
		}
		return reply.Structured
	}
	return envelope.Data
}

func TestMCPToolsThroughStdioAndDisposableService(t *testing.T) {
	f := newTeam(t)
	worker := newMCPClientTest(t, f, legacyCopy(t, f.workerFile), false)
	orch := newMCPClientTest(t, f, f.orchFile, true)
	worker.tool("session_inspect", map[string]any{}, "")
	worker.tool("task_list", map[string]any{"limit": 1}, "")
	worker.tool("task_show", map[string]any{"task_id": f.task}, "")
	worker.tool("task_create", map[string]any{"title": "Forbidden", "operation_id": "mcp-denied-create"}, "forbidden")
	worker.tool("task_show", map[string]any{"task_id": f.task, "workspace_id": f.workspace}, "invalid_input")
	create := map[string]any{"title": "MCP linked", "after": []string{f.task}, "assign": []string{"01-orchestrator"}, "operation_id": "mcp-linked-create"}
	var made stack.Receipt
	data := orch.tool("task_create", create, "")
	if json.Unmarshal(data, &made) != nil || made.Revision != 2 || !bytes.Equal(data, orch.tool("task_create", create, "")) {
		t.Fatal("atomic linked create or operation replay")
	}
	cli := f.receipt(f.orchFile, "task", "create", "--title", "MCP linked", "--after", f.task, "--assign", "01-orchestrator", "--operation-id", "mcp-linked-create")
	if cli != made {
		t.Fatal("MCP and CLI must share operation receipts")
	}
	create["title"] = "changed"
	orch.tool("task_create", create, "operation_conflict")
	orch.tool("task_assign", map[string]any{"task_id": made.ResourceID, "agent_id": "02-worker", "expected_revision": 2, "operation_id": "mcp-assign-worker"}, "")
	worker.tool("task_dependencies", map[string]any{"task_id": made.ResourceID}, "")
	orch.tool("mail_read", map[string]any{"task_id": f.task}, "forbidden")
	status := map[string]any{"task_id": f.task, "expected_revision": 2, "progress": "working", "operation_id": "mcp-working-task"}
	worker.tool("task_status", status, "")
	status["operation_id"] = "mcp-stale-update"
	errJSON := worker.tool("task_status", status, "conflict")
	_, cliError, code := f.run(f.workerFile, "task", "status", "--task", f.task, "--expected-revision", "2", "--progress", "working", "--operation-id", "mcp-stale-update")
	var a, b any
	_ = json.Unmarshal(errJSON, &a)
	_ = json.Unmarshal([]byte(cliError), &b)
	if code != 1 || !reflect.DeepEqual(a, b) {
		t.Fatal("MCP must retain the CLI error and conflict fields")
	}
	worker.tool("task_report", map[string]any{"task_id": f.task, "expected_revision": 3, "report": "Stdio report\nsecond line", "operation_id": "mcp-report-task"}, "")
	worker.tool("task_attention_set", map[string]any{"task_id": f.task, "expected_revision": 4, "question": "Which release?", "operation_id": "mcp-question-task"}, "")
	worker.tool("task_attention_resolve", map[string]any{"task_id": f.task, "expected_revision": 5, "note": "Use current", "operation_id": "mcp-resolve-task"}, "")
	worker.tool("mail_send", map[string]any{"task_id": f.task, "body": "Ready @02-worker", "operation_id": "mcp-mail-message"}, "")
	var page stack.MailPage
	if json.Unmarshal(worker.tool("mail_read", map[string]any{"task_id": f.task, "limit": 1}, ""), &page) != nil || len(page.Messages) != 1 {
		t.Fatal("mail read")
	}
	check := worker.tool("mail_check", map[string]any{"cursors": map[string]string{f.task: page.Cursor}}, "")
	var mail stack.MailCheckPage
	if json.Unmarshal(check, &mail) != nil {
		t.Fatal("mail check")
	}
	for _, task := range mail.Tasks {
		if task.TaskID == f.task && len(task.Messages) != 0 {
			t.Fatal("mail check lost cursor")
		}
	}
	var h handoff
	if json.Unmarshal(worker.tool("task_handoff", map[string]any{"task_id": f.task, "limit": 1}, ""), &h) != nil || h.Task.Report != "Stdio report\nsecond line" || len(h.Mail.Messages) != 1 {
		t.Fatal("handoff")
	}
	var third stack.Receipt
	_ = json.Unmarshal(orch.tool("task_create", map[string]any{"title": "Third", "operation_id": "mcp-third-create"}, ""), &third)
	var session stack.Session
	_ = json.Unmarshal(orch.tool("session_inspect", map[string]any{}, ""), &session)
	orch.tool("dependency_add", map[string]any{"prerequisite_id": made.ResourceID, "dependent_id": third.ResourceID, "expected_graph_revision": session.GraphRevision, "operation_id": "mcp-dependency-add"}, "")
	worker.tool("task_status", map[string]any{"task_id": f.task, "expected_revision": 6, "progress": "finished", "summary": "All delivered", "operation_id": "mcp-finish-task"}, "")
}

func TestMCPRequiresExplicitAgentCredential(t *testing.T) {
	f := newTeam(t)
	for _, flags := range [][]string{nil, {"--credential-file", filepath.Join(f.state, "operator.json")}, {"--credential-file", f.workerFile, "--as", "operator"}, {"--credential-file", f.workerFile, "--as", "browser"}} {
		args := append([]string{"mcp", "--state-dir", f.state}, flags...)
		var out, errs bytes.Buffer
		if run(args, &out, &errs) == 0 || out.Len() != 0 || !json.Valid(errs.Bytes()) {
			t.Fatal("MCP accepted non-Agent mode or wrote diagnostics on stdout")
		}
	}
	var invalidOut, invalidErr bytes.Buffer
	if code := run([]string{"mcp", "--state-dir", f.state, "--credential-file", f.workerFile, "--session", f.workspace}, &invalidOut, &invalidErr); code != 2 || invalidOut.Len() != 0 {
		t.Fatal("MCP allowed a context that differs from its credential")
	}
	// EOF ends a valid stdio connection cleanly, with no startup banner.
	command := child(t, "mcp", "--state-dir", f.state, "--credential-file", f.workerFile)
	command.Stdin = strings.NewReader("")
	var out, errs bytes.Buffer
	command.Stdout, command.Stderr = &out, &errs
	if err := command.Run(); err != nil || out.Len() != 0 || errs.Len() != 0 {
		t.Fatalf("MCP EOF: %v", err)
	}
}
