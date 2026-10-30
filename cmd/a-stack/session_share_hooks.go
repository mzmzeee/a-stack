package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type shareHook struct {
	File, Harness, Command string
	Identity               hookIdentity
}

func shareHookInventory(worktrees []string) ([]shareHook, error) {
	files := map[string]string{}
	for _, flags := range []hookFlags{{claude: true}, {codex: true}, {pi: true}, {opencode: true}} {
		settings, err := hookSettings(flags, false)
		if err != nil {
			return nil, err
		}
		for harness, file := range settings {
			files[file] = harness
		}
	}
	for _, tree := range worktrees {
		if tree != "" {
			files[filepath.Join(tree, ".claude", "settings.local.json")] = "claude"
			files[filepath.Join(tree, ".codex", "hooks.json")] = "codex"
		}
	}
	result := []shareHook{}
	for file, harness := range files {
		commands, err := installedHookCommands(file)
		if err != nil {
			return nil, err
		}
		for _, command := range commands {
			identity, ok := installedHookIdentity(command, harness)
			if ok {
				result = append(result, shareHook{file, harness, command, identity})
			}
		}
	}
	return result, nil
}

func prepareShareAgents(ctx context.Context, state string, source, dest *stack.Client, bundle stack.TransferBundle, manifest string) (map[string]shareAgent, error) {
	ref := stack.Context{WorkspaceID: bundle.Session.WorkspaceID, SessionID: bundle.Session.ID}
	agents, err := listPages[stack.Agent](ctx, source, "/v1/agent/list", ref)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return map[string]shareAgent{}, nil
	}
	paths := map[string]string{}
	if manifest != "" {
		data, e := os.ReadFile(manifest)
		if e != nil {
			return nil, e
		}
		if len(data) > 1<<20 || json.Unmarshal(data, &paths) != nil {
			return nil, invalid("--agent-files must be a JSON object mapping Agent IDs to private credential paths")
		}
	}
	trees := []string{}
	for _, agent := range agents {
		trees = append(trees, agent.WorktreePath)
	}
	hooks, err := shareHookInventory(trees)
	if err != nil {
		return nil, err
	}
	for _, hook := range hooks {
		if paths[hook.Identity.agent] == "" {
			paths[hook.Identity.agent] = hook.Identity.credential
		}
	}
	result := map[string]shareAgent{}
	for _, agent := range agents {
		candidates := []string{paths[agent.ID], filepath.Join(filepath.Dir(state), "a-stack-agents", ref.SessionID, agent.Name+".json"), filepath.Join(filepath.Dir(state), "a-stack-agents", agent.Name+".json")}
		for _, base := range append([]string(nil), candidates...) {
			if base != "" {
				matches, _ := filepath.Glob(base + ".*.json")
				candidates = append(candidates, matches...)
			}
		}
		old := ""
		for _, path := range candidates {
			if path == "" {
				continue
			}
			credential, e := stack.ReadCredential(path)
			if e != nil || credential.Kind != stack.AgentCapability {
				continue
			}
			own := *source
			own.Credential = credential
			session, e := inspectSession(ctx, &own, ref)
			if e == nil && session.YourAgentID == agent.ID {
				old = path
				break
			}
		}
		if old == "" {
			return nil, invalid("cannot locate the private credential for Agent " + agent.Name + "; give --agent-files with its explicit path before sharing")
		}
		// A source enrollment may migrate only with its exact local machine and
		// credential generation. A folder match is never enrollment evidence.
		file, e := enrollmentFile(agent.ID)
		if e != nil {
			return nil, e
		}
		if data, e := os.ReadFile(file); e == nil {
			var enrollment hookEnrollment
			machine, e := hookMachine(false)
			if e != nil || json.Unmarshal(data, &enrollment) != nil || machine == "" || enrollment.Machine != machine || enrollment.Credential != credentialGeneration(old) || enrollment.Revision != agent.Revision || enrollment.Session == "" {
				return nil, invalid("Agent enrollment does not match this machine, credential and revision; explicitly enroll it before sharing")
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		newPath := old + "." + bundle.TransferID + ".json"
		request := stack.AgentRegisterRequest{Context: ref, OperationID: bundle.TransferID, Name: agent.Name, WorktreePath: agent.WorktreePath}
		hash, e := stack.PrepareAgentCredential(newPath, dest.Discovery.DatabaseID, "session transfer", bundle.TransferID, request)
		if e != nil {
			return nil, e
		}
		result[agent.ID] = shareAgent{Old: old, New: newPath, Hash: hash}
	}
	return result, nil
}

func migrateShareHooks(agents map[string]shareAgent, source, dest stack.Context) error {
	trees := []string{}
	for id := range agents {
		file, err := enrollmentFile(id)
		if err != nil {
			return err
		}
		if data, e := os.ReadFile(file); e == nil {
			var enrollment hookEnrollment
			if json.Unmarshal(data, &enrollment) == nil {
				trees = append(trees, enrollment.Worktree)
			}
		}
	}
	hooks, err := shareHookInventory(trees)
	if err != nil {
		return err
	}
	files := map[string][]shareHook{}
	for _, hook := range hooks {
		agent, ok := agents[hook.Identity.agent]
		if ok && hook.Identity.credential == agent.Old && strings.Contains(hook.Command, " --session "+source.SessionID) {
			files[hook.File] = append(files[hook.File], hook)
		}
	}
	for file, hooks := range files {
		info, e := os.Lstat(file)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return invalid("unsafe hook settings file")
		}
		for parent := filepath.Dir(file); ; parent = filepath.Dir(parent) {
			entry, e := os.Lstat(parent)
			if e != nil {
				return e
			}
			if entry.Mode()&os.ModeSymlink != 0 {
				return invalid("hook settings path contains a symlink")
			}
			if filepath.Dir(parent) == parent {
				break
			}
		}
		data, e := os.ReadFile(file)
		if e != nil {
			return e
		}
		for _, hook := range hooks {
			agent := agents[hook.Identity.agent]
			command := strings.Replace(hook.Command, "--credential-file "+quotePath(agent.Old), "--credential-file "+quotePath(agent.New), 1)
			command = strings.Replace(command, "--workspace "+source.WorkspaceID, "--workspace "+dest.WorkspaceID, 1)
			before, _ := json.Marshal(hook.Command)
			after, _ := json.Marshal(command)
			data = bytes.ReplaceAll(data, before, after)
		}
		if e = writeAtomic(file, data, info.Mode().Perm()); e != nil {
			return e
		}
	}
	for id, agent := range agents {
		file, e := enrollmentFile(id)
		if e != nil {
			return e
		}
		data, e := os.ReadFile(file)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		var enrollment hookEnrollment
		machine, e := hookMachine(false)
		if e != nil || json.Unmarshal(data, &enrollment) != nil || machine == "" || enrollment.Machine != machine || enrollment.Session == "" {
			return invalid("cannot migrate an enrollment from another machine")
		}
		oldGeneration, newGeneration := credentialGeneration(agent.Old), credentialGeneration(agent.New)
		if enrollment.Credential == newGeneration {
			continue
		}
		if enrollment.Credential != oldGeneration {
			return invalid("enrollment credential changed during transfer")
		}
		enrollment.Credential = newGeneration
		data, e = json.Marshal(enrollment)
		if e != nil {
			return e
		}
		if e = writeAtomic(file, data, 0600); e != nil {
			return e
		}
	}
	return nil
}
