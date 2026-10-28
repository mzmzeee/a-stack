package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// Enrollment is local to this machine and credential generation. Reading it
// never creates cache folders: unrelated hook events must leave no footprint.
type hookEnrollment struct {
	Machine, Credential, Harness, Session, Subagent, Worktree, Runtime string
	Revision                                                           int64
}

func enrollmentFile(agent string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	if !stack.ValidID(agent) {
		return "", invalid("invalid Agent ID")
	}
	return filepath.Join(hookCacheDir(cache, agent), "enrollment"), nil
}

// The random machine marker stays local, outside per-Agent enrollment files.
// The hostname also rejects an accidentally copied cache on a renamed host.
func hookMachine(create bool) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	file := filepath.Join(filepath.Dir(hookCacheDir(cache, "agent")), "machine")
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) && create {
		if err = os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return "", err
		}
		var token [32]byte
		if _, err = rand.Read(token[:]); err != nil {
			return "", err
		}
		f, openErr := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr == nil {
			_, err = f.Write([]byte(hex.EncodeToString(token[:])))
			closeErr := f.Close()
			if err != nil {
				return "", err
			}
			if closeErr != nil {
				return "", closeErr
			}
		} else if !os.IsExist(openErr) {
			return "", openErr
		}
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return "", err
	}
	if len(data) != 64 {
		return "", invalid("invalid hook machine marker; remove it and enroll again")
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "", invalid("cannot identify this machine")
	}
	return host + ":" + string(data), nil
}

func credentialGeneration(file string) string {
	c, err := stack.ReadCredential(file)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(c.Token))
	return hex.EncodeToString(sum[:])
}

func enrollHook(id hookIdentity, harness, session, subagent string, revision int64) error {
	if revision < 1 {
		return invalid("current Agent revision required for enrollment")
	}
	if session == "" || len(session) > 4096 || strings.ContainsAny(session, "\r\n\x00") {
		return invalid("a native harness session ID is required")
	}
	if harness != "claude" && harness != "codex" && harness != "pi" && harness != "opencode" {
		return invalid("choose exactly one harness to enroll")
	}
	if subagent != "" && harness != "claude" {
		return invalid("native subagents are supported only for Claude Code")
	}
	worktree, err := stack.ResolvePath(id.worktree)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil || holdsHome(worktree, home) {
		return invalid("cannot enroll hooks for home or an ancestor")
	}
	machine, err := hookMachine(true)
	if err != nil || machine == "" {
		return invalid("cannot identify this machine")
	}
	generation := credentialGeneration(id.credential)
	if generation == "" {
		return invalid("cannot read Agent credential")
	}
	file, err := enrollmentFile(id.agent)
	if err != nil {
		return err
	}
	data, err := json.Marshal(hookEnrollment{machine, generation, harness, session, subagent, worktree, id.runtimeID, revision})
	if err != nil {
		return err
	}
	return writeAtomic(file, data, 0o600)
}

func enrolledHook(id hookIdentity, in hookInput) int64 {
	file, err := enrollmentFile(id.agent)
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(file)
	var e hookEnrollment
	if err != nil || json.Unmarshal(data, &e) != nil {
		return 0
	}
	machine, err := hookMachine(false)
	if err == nil && machine != "" && machine == e.Machine &&
		e.Harness == id.harness && e.Session != "" && e.Session == in.SessionID &&
		e.Subagent == in.AgentID && e.Worktree == id.worktree && (id.runtimeID == "" || e.Runtime == id.runtimeID) &&
		e.Credential != "" && e.Credential == credentialGeneration(id.credential) {
		return e.Revision
	}
	return 0
}

func (h *hookInstall) enroll(ctx context.Context) error {
	session := h.flags.nativeSession
	if session == "" {
		if h.flags.nativeAgent != "" {
			return invalid("--native-agent needs --native-session")
		}
		return nil
	}
	harness := ""
	for name, enabled := range map[string]bool{"claude": h.flags.claude, "codex": h.flags.codex, "pi": h.flags.pi, "opencode": h.flags.opencode} {
		if enabled {
			if harness != "" {
				return invalid("choose exactly one harness to enroll")
			}
			harness = name
		}
	}
	state := h.identity.state
	if state == "" {
		var err error
		state, err = stack.DefaultStateDir()
		if err != nil {
			return err
		}
	}
	client, err := stack.NewClient(state, h.identity.credential, stack.AgentCapability)
	if err != nil {
		return err
	}
	current, err := inspectSession(ctx, client, h.identity.ref)
	if err != nil {
		return err
	}
	if current.Closed || current.YourAgentID != h.identity.agent {
		return invalid("enrollment needs this Agent's open Session")
	}
	agents, err := listPages[stack.Agent](ctx, client, "/v1/agent/list", h.identity.ref)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.ID == h.identity.agent {
			return enrollHook(h.identity, harness, session, h.flags.nativeAgent, a.Revision)
		}
	}
	return invalid("Agent not found")
}
