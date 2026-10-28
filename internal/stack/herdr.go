package stack

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const noticeText = "New a-stack Task mail may mention you. Only if this runtime has its own verified a-stack Agent registration, load the a-stack skill and check your own mail. Otherwise ignore this notice; do not register or discover credentials."

// herdrProtocol is the private API protocol of Herdr 0.9.5, which the native
// fixtures used. a-stack accepts any Herdr whose running server reports this
// protocol and a compatible CLI and endpoint, not one exact release string.
const herdrProtocol = 22

var herdrName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// verifiedHarnesses holds, per harness, the version whose queued input a
// native fixture verified with Herdr protocol 22 (docs/notifications.md).
// Notices accept that minor line from that version up: codex >=0.164.0 <0.165,
// pi >=1.4.0 <1.5, claude >=2.1.310 <2.2.
// Herdr metadata reports identity, not the harness version; herdr bind or the
// runtime file declares it.
var verifiedHarnesses = map[string]string{"codex": "0.164.0", "pi": "1.4.0", "claude": "2.1.310"}

// versionParts parses a plain MAJOR.MINOR.PATCH version without prerelease.
func versionParts(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || len(part) > 9 || (len(part) > 1 && part[0] == '0') || strings.ContainsAny(part, "+-") {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
func verifiedHarness(b RuntimeBinding) bool {
	floor, ok := verifiedHarnesses[b.Harness]
	v, valid := versionParts(b.HarnessVersion)
	f, _ := versionParts(floor)
	return ok && valid && v[0] == f[0] && v[1] == f[1] && v[2] >= f[2]
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
	TabID      string `json:"tab_id"`
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
	return a.Name == b.HerdrAlias && a.PaneID == b.PaneID && a.TerminalID == b.TerminalID && a.Agent == b.Harness && nativeSession(b.Harness, a.Session.Agent, a.Session.Kind, a.Session.Source, a.Session.Value) && a.Session.Value == b.NativeSessionID
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

// compatible asks the running server at the recorded endpoint for its
// protocol, instead of comparing the CLI's release string.
func (h *herdrAdapter) compatible(ctx context.Context, b RuntimeBinding) string {
	out, _, _, err := h.command(ctx, b, "status", "server", "--json")
	var status struct {
		Running            bool `json:"running"`
		Protocol           int  `json:"protocol"`
		Compatible         bool `json:"compatible"`
		EndpointCompatible bool `json:"endpoint_compatible"`
	}
	if err != nil || !utf8.Valid(out) || json.Unmarshal(out, &status) != nil || !status.Running {
		return "herdr_unavailable"
	}
	if status.Protocol != herdrProtocol || !status.Compatible || !status.EndpointCompatible {
		return "unsupported_herdr"
	}
	return ""
}

// current checks a complete binding against Herdr's current metadata, as
// before every notice. It returns a safe reason when they differ.
func (h *herdrAdapter) current(ctx context.Context, b RuntimeBinding) (herdrAgent, string) {
	if reason := h.compatible(ctx, b); reason != "" {
		return herdrAgent{}, reason
	}
	out, _, _, err := h.command(ctx, b, "agent", "get", b.HerdrAlias)
	if err != nil {
		return herdrAgent{}, "runtime_unavailable"
	}
	var response herdrResponse
	if !utf8.Valid(out) || json.Unmarshal(out, &response) != nil || !response.Result.Agent.matches(b) {
		return herdrAgent{}, "identity_changed"
	}
	return response.Result.Agent, ""
}
func (h *herdrAdapter) verify(ctx context.Context, b RuntimeBinding) string {
	if !completeHerdrBinding(b) {
		return "binding_incomplete"
	}
	if !h.verified(b) {
		return "unsupported_harness"
	}
	agent, reason := h.current(ctx, b)
	if reason != "" {
		return reason
	}
	switch agent.Status {
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
