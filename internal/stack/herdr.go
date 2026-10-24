package stack

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const noticeText = "New a-stack Task mail mentions you. Run your own a-stack mail check with your registered Workspace, Session and Agent credential."
const herdrVersion = "herdr 0.9.5"

var herdrName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// The dedicated native fixture proved working/idle input for this exact pair
// with Herdr 0.9.5. Per-target metadata reports identity, not harness version;
// enrollment declares the version. See docs/notifications.md for proof limits.
func verifiedHarness(b RuntimeBinding) bool {
	return b.Harness == "codex" && b.HarnessVersion == "0.164.0"
}

type herdrAdapter struct {
	executable string
	verified   func(RuntimeBinding) bool
	timeout    time.Duration
}

func newHerdr(executable string) *herdrAdapter {
	return &herdrAdapter{executable: executable, verified: verifiedHarness, timeout: 5 * time.Second}
}

type herdrAgent struct {
	Name       string `json:"name"`
	PaneID     string `json:"pane_id"`
	TerminalID string `json:"terminal_id"`
	Agent      string `json:"agent"`
	Status     string `json:"agent_status"`
	Session    struct {
		Agent  string `json:"agent"`
		Kind   string `json:"kind"`
		Source string `json:"source"`
		Value  string `json:"value"`
	} `json:"agent_session"`
}
type herdrResponse struct {
	Result struct {
		Agent herdrAgent `json:"agent"`
	} `json:"result"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func safeBindingField(s string) bool {
	return s != "" && len(s) <= 512 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func completeHerdrBinding(b RuntimeBinding) bool {
	return safeBindingField(b.InstanceID) && herdrName.MatchString(b.HerdrAlias) && filepath.IsAbs(b.Endpoint) && safeBindingField(b.Endpoint) && safeBindingField(b.PaneID) && !strings.HasPrefix(b.PaneID, "-") && safeBindingField(b.TerminalID) && safeBindingField(b.NativeSessionID) && safeBindingField(b.Harness) && safeBindingField(b.HarnessVersion)
}
func (a herdrAgent) matches(b RuntimeBinding) bool {
	return a.Name == b.HerdrAlias && a.PaneID == b.PaneID && a.TerminalID == b.TerminalID && a.Agent == b.Harness && a.Session.Agent == b.Harness && a.Session.Kind == "id" && a.Session.Source == "herdr:"+b.Harness && a.Session.Value == b.NativeSessionID
}

// Capture bounded output only for decoding allowlisted metadata/codes. Never
// persist or return the CLI's raw output, terminal title, cwd or diagnostics.
type boundedOutput struct {
	data     []byte
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 32*1024 - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (h *herdrAdapter) command(ctx context.Context, b RuntimeBinding, args ...string) ([]byte, []byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, h.executable, args...)
	// No inherited HERDR_SESSION/--session, pane, machine or remote selectors.
	// Captured socket and explicit target select every metadata/submission call.
	for _, name := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "LANG"} {
		if value, ok := os.LookupEnv(name); ok {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	command.Env = append(command.Env, "HERDR_SOCKET_PATH="+b.Endpoint)
	var stdout, stderr boundedOutput
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.WaitDelay = 250 * time.Millisecond
	err := command.Run()
	if stdout.overflow || stderr.overflow {
		return nil, nil, command.Process != nil, failure("integration_unavailable", "Herdr output exceeded the metadata limit")
	}
	return stdout.data, stderr.data, command.Process != nil, err
}
func (h *herdrAdapter) verify(ctx context.Context, b RuntimeBinding) string {
	if !completeHerdrBinding(b) {
		return "binding_incomplete"
	}
	if !h.verified(b) {
		return "unsupported_harness"
	}
	out, _, _, err := h.command(ctx, b, "--version")
	if err != nil {
		return "herdr_unavailable"
	}
	if strings.TrimSpace(string(out)) != herdrVersion {
		return "unsupported_herdr"
	}
	out, _, _, err = h.command(ctx, b, "agent", "get", b.HerdrAlias)
	if err != nil {
		return "runtime_unavailable"
	}
	var response herdrResponse
	if !utf8.Valid(out) || json.Unmarshal(out, &response) != nil || !response.Result.Agent.matches(b) {
		return "identity_changed"
	}
	switch response.Result.Agent.Status {
	case "working", "idle", "done":
		return ""
	default:
		return "runtime_unavailable"
	}
}
func (h *herdrAdapter) submit(ctx context.Context, b RuntimeBinding) (NoticeState, string) {
	// Herdr 0.9.5 permits --timeout only with --wait. Normal queue submission
	// uses neither; command's context bounds the process without an idle wait.
	out, diagnostic, started, err := h.command(ctx, b, "agent", "prompt", b.PaneID, noticeText)
	if !started {
		return NoticePending, "preinput_failure"
	}
	var response herdrResponse
	if err != nil {
		if utf8.Valid(diagnostic) && json.Unmarshal(diagnostic, &response) == nil && response.Error.Code == "agent_blocked" {
			return NoticePending, "runtime_blocked"
		}
		return NoticeUncertain, "delivery_uncertain"
	}
	if !utf8.Valid(out) || json.Unmarshal(out, &response) != nil || response.Error.Code != "" || !response.Result.Agent.matches(b) {
		return NoticeUncertain, "delivery_uncertain"
	}
	return NoticeSubmitted, "submitted"
}
