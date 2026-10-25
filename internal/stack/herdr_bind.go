package stack

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// bindableHarnesses are the harnesses whose Herdr integration reports a
// native session and whose `--version` herdr bind can read. Notices still need
// a verified line in verifiedHarnesses.
var bindableHarnesses = map[string]bool{"claude": true, "codex": true}
var harnessVersionPattern = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

// agent runs one Herdr agent command and decodes the agent it returns.
func (h *herdrAdapter) agent(ctx context.Context, b RuntimeBinding, args ...string) (herdrAgent, bool) {
	out, _, _, err := h.command(ctx, b, args...)
	var response herdrResponse
	if err != nil || !utf8.Valid(out) || json.Unmarshal(out, &response) != nil {
		return herdrAgent{}, false
	}
	return response.Result.Agent, true
}

// HerdrBind reads the caller's own Herdr pane and returns the binding that
// `a-stack herdr bind` attaches. It needs the variables Herdr sets in each
// pane. It renames only that pane's agent, and only when it has no name. It
// never reports agent state or sessions to Herdr.
func HerdrBind(ctx context.Context, executable, agentName string, getenv func(string) string) (*RuntimeBinding, error) {
	pane, socket := getenv("HERDR_PANE_ID"), getenv("HERDR_SOCKET_PATH")
	if getenv("HERDR_ENV") != "1" || !safeBindingField(pane) || strings.HasPrefix(pane, "-") || !filepath.IsAbs(socket) || !safeBindingField(socket) {
		return nil, failure("invalid_input", "run herdr bind inside your own Herdr pane: HERDR_ENV, HERDR_PANE_ID or HERDR_SOCKET_PATH is missing")
	}
	h := newHerdr(executable)
	b := RuntimeBinding{Endpoint: socket, PaneID: pane}
	if reason := h.compatible(ctx, b); reason != "" {
		return nil, failure("integration_unavailable", "Herdr is not running or not supported: "+reason)
	}
	agent, ok := h.agent(ctx, b, "agent", "get", pane)
	if !ok || !bindableHarnesses[agent.Agent] {
		return nil, failure("integration_missing", "Herdr sees no Claude Code or Codex in this pane; start your harness in it, then bind again")
	}
	harness := agent.Agent
	if agent.Session.Agent != harness || agent.Session.Kind != "id" || agent.Session.Source != "herdr:"+harness || agent.Session.Value == "" {
		return nil, failure("integration_missing", "Herdr has no session for this pane; run herdr integration install "+harness+", restart "+harness+" in this pane, then bind again")
	}
	alias := agent.Name
	if alias == "" {
		alias = "a-" + strings.ToLower(agentName)
		alias = alias[:min(len(alias), 32)]
		renamed, ok := h.agent(ctx, b, "agent", "rename", pane, alias)
		if !ok || renamed.Name != alias || renamed.TerminalID != agent.TerminalID {
			return nil, failure("conflict", "Herdr did not take the name "+alias+" for this pane; another agent may use it")
		}
	}
	version, err := harnessVersion(ctx, harness)
	if err != nil {
		return nil, err
	}
	binding := &RuntimeBinding{InstanceID: "herdr:" + agent.TerminalID, HerdrAlias: alias, Endpoint: socket, PaneID: agent.PaneID, TerminalID: agent.TerminalID, NativeSessionID: agent.Session.Value, Harness: harness, HarnessVersion: version}
	if len(binding.InstanceID) > 128 || !completeHerdrBinding(*binding) {
		return nil, failure("integration_unavailable", "Herdr returned incomplete pane data")
	}
	return binding, nil
}

// harnessVersion runs the harness's own `--version` in the caller's
// environment and keeps only the first MAJOR.MINOR.PATCH it prints.
func harnessVersion(ctx context.Context, harness string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, harness, "--version")
	var stdout boundedOutput
	command.Stdout = &stdout
	command.WaitDelay = 250 * time.Millisecond
	err := command.Run()
	version := harnessVersionPattern.FindString(string(stdout.data))
	if _, ok := versionParts(version); err != nil || !ok {
		return "", failure("integration_unavailable", harness+" --version printed no version")
	}
	return version, nil
}
