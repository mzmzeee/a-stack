package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func installedHookIdentity(command, harness string) (hookIdentity, bool) {
	command = strings.TrimSuffix(command, " --harness "+harness)
	if !legacyHookCommand(command) {
		return hookIdentity{}, false
	}

	m := legacyHookPattern.FindStringSubmatch(command)
	unquote := func(s string) string { return filepath.FromSlash(strings.ReplaceAll(s[1:len(s)-1], `'\''`, `'`)) }
	return hookIdentity{credential: unquote(m[2]), agent: m[3], worktree: unquote(m[4]), harness: harness, noMailNudge: strings.Contains(command, " --no-mail-nudge --worktree ")}, true
}

func installedHookCommands(file string) ([]string, error) {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	defer f.Close()

	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
	}
	if err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&doc); err != nil {
		return nil, invalid("cannot read hook settings: " + file)
	}

	var commands []string
	for _, groups := range doc.Hooks {
		for _, group := range groups {
			for _, h := range group.Hooks {
				if h.Type == "command" {
					commands = append(commands, h.Command)
				}
			}
		}
	}

	return commands, nil
}

type hookStatusRow struct {
	UrgentMail       bool   `json:"urgent_mail"`
	Harness          string `json:"harness"`
	Worktree         string `json:"worktree,omitempty"`
	Installed        bool   `json:"installed"`
	Tagged           bool   `json:"tagged"`
	AgentEnrolled    bool   `json:"agent_enrolled"`
	SessionKnown     bool   `json:"session_known"`
	Enrolled         bool   `json:"enrolled"`
	MigrationPending bool   `json:"migration_pending"`
}

func hookStatus(ctx context.Context, id hookIdentity, flags hookFlags, machine bool, out io.Writer) error {
	h := hookInstall{identity: id}
	if _, err := h.command(ctx); err != nil {
		return err
	}

	id = h.identity

	state := id.state
	var err error
	if state == "" {
		state, err = stack.DefaultStateDir()
		if err != nil {
			return err
		}
	}

	client, err := stack.NewClient(state, id.credential, stack.AgentCapability)
	if err != nil {
		return err
	}

	session, err := inspectSession(ctx, client, id.ref)
	if err != nil {
		return err
	}

	if session.YourAgentID != id.agent {
		return invalid("hook status requires this Agent's own identity")
	}

	agents, err := listPages[stack.Agent](ctx, client, "/v1/agent/list", id.ref)
	if err != nil {
		return err
	}

	var revision int64
	for _, a := range agents {
		if a.ID == id.agent {
			revision = a.Revision
		}
	}

	file, err := enrollmentFile(id.agent)
	if err != nil {
		return err
	}

	var e hookEnrollment
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &e)
	}

	pending := pendingHooks(id.credential)

	roots := map[string]bool{}
	if cwd, err := os.Getwd(); err == nil {
		roots[cwd] = true
	}

	if e.Worktree != "" {
		roots[e.Worktree] = true
	}

	for _, p := range pending {
		roots[p.Worktree] = true
	}

	generation := credentialGeneration(id.credential)

	all := !flags.claude && !flags.codex && !flags.pi && !flags.opencode
	if flags.nativeSession != "" && all {
		return invalid("choose a harness with --native-session")
	}

	rows := []hookStatusRow{}
	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		selected := map[string]bool{"claude": flags.claude, "codex": flags.codex, "pi": flags.pi, "opencode": flags.opencode}
		if !all && !selected[harness] {
			continue
		}

		f := hookFlags{claude: harness == "claude", codex: harness == "codex", pi: harness == "pi", opencode: harness == "opencode", nativeSession: flags.nativeSession, nativeAgent: flags.nativeAgent}
		known := f.detectNativeSession() == nil

		settings, err := hookSettings(f, false)
		if err != nil {
			return err
		}

		files := map[string]bool{settings[harness]: true}
		for root := range roots {
			if harness == "claude" {
				files[filepath.Join(root, ".claude", "settings.local.json")] = true
			}

			if harness == "codex" {
				files[filepath.Join(root, ".codex", "hooks.json")] = true
			}
		}

		found := map[string]*hookStatusRow{}
		for file := range files {
			commands, err := installedHookCommands(file)
			if err != nil {
				return err
			}

			for _, command := range commands {
				installed, ok := installedHookIdentity(command, harness)
				if !ok || installed.agent != id.agent || credentialGeneration(installed.credential) != generation {
					continue
				}

				worktree, err := stack.ResolvePath(installed.worktree)
				if err != nil {
					return err
				}

				row := found[worktree]
				if row == nil {
					row = &hookStatusRow{Harness: harness, Worktree: worktree, Installed: true, SessionKnown: known}
					found[worktree] = row
				}

				row.Tagged = row.Tagged || strings.HasSuffix(command, " --harness "+harness)
				check := id
				check.harness = harness
				check.worktree = worktree
				home, _ := os.UserHomeDir()
				row.AgentEnrolled = !session.Closed && revision > 0 && !holdsHome(worktree, home) && enrolledHook(check, hookInput{SessionID: e.Session, AgentID: e.Subagent}) == revision

				row.Enrolled = row.Tagged && row.AgentEnrolled && known && e.Session == f.nativeSession && e.Subagent == f.nativeAgent
				row.UrgentMail = row.Enrolled && !installed.noMailNudge
				for _, p := range pending {
					if p.Harness == harness && p.Worktree == worktree {
						row.MigrationPending = true
					}
				}
			}
		}

		if len(found) == 0 {
			rows = append(rows, hookStatusRow{Harness: harness, SessionKnown: known})
		}

		for _, row := range found {
			rows = append(rows, *row)
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Harness != rows[j].Harness {
			return rows[i].Harness < rows[j].Harness
		}

		return rows[i].Worktree < rows[j].Worktree
	})

	if machine {
		return json.NewEncoder(out).Encode(map[string]any{"data": map[string]any{"agent_id": id.agent, "hooks": rows}})
	}

	for _, row := range rows {
		current := "not enrolled"
		if row.Enrolled {
			current = "enrolled"
		} else if !row.SessionKnown {
			current = "unknown (native session metadata unavailable)"
		}

		if _, err := fmt.Fprintf(out, "%s: installed=%t, Agent enrolled=%t, current session=%s, migration pending=%t, urgent mail=%t (requires enrolled mail hooks) %s\n", row.Harness, row.Installed, row.AgentEnrolled, current, row.MigrationPending, row.UrgentMail, printable(row.Worktree)); err != nil {
			return err
		}
	}

	return nil
}
