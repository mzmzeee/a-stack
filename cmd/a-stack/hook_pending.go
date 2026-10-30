package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type hookPending struct{ Machine, Worktree, Harness string }

func pendingHookDir(credential string) string {
	generation := credentialGeneration(credential)
	cache, err := os.UserCacheDir()
	if generation == "" || err != nil {
		return ""
	}

	return filepath.Join(filepath.Dir(hookCacheDir(cache, "agent")), "pending", generation)
}

func pendingHookFile(credential, worktree, harness string) string {
	dir := pendingHookDir(credential)
	if dir == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(worktree + "\x00" + harness))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}

func markHookPending(id hookIdentity) error {
	worktree, err := stack.ResolvePath(id.worktree)
	if err != nil {
		return err
	}

	file := pendingHookFile(id.credential, worktree, id.harness)
	if file == "" {
		return invalid("cannot record hook migration; restore the installed Agent credential and retry refresh")
	}

	machine, err := hookMachine(true)
	if err != nil {
		return err
	}

	data, err := json.Marshal(hookPending{machine, worktree, id.harness})
	if err != nil {
		return err
	}

	if old, err := os.ReadFile(file); err == nil && string(old) == string(data) {
		return nil
	}

	return writeAtomic(file, data, 0o600)
}

func pendingHooks(credential string) []hookPending {
	dir := pendingHookDir(credential)
	if dir == "" {
		return nil
	}

	machine, err := hookMachine(false)
	if err != nil {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var pending []hookPending
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		var p hookPending
		if err != nil || json.Unmarshal(data, &p) != nil || p.Machine != machine || !filepath.IsAbs(p.Worktree) {
			continue
		}

		switch p.Harness {
		case "claude", "codex", "pi", "opencode":
			pending = append(pending, p)
		}
	}

	return pending
}

func pendingHookNotice(credential string, stderr io.Writer) {
	pending := pendingHooks(credential)
	if len(pending) == 0 {
		return
	}

	var flags []string
	for _, h := range []string{"claude", "codex", "pi", "opencode"} {
		for _, p := range pending {
			if p.Harness == h {
				flags = append(flags, "--"+h)
				break
			}
		}
	}

	fmt.Fprintf(stderr, "a-stack: hooks are installed but not enrolled; run me hook enroll %s from your own native session in its hook worktree (see me hook status).\n", strings.Join(flags, " or "))
}

// One Agent uses one native harness; the default installer also installs the
// unused harness. Enrollment completes migration for this credential/worktree.
func clearHookPending(id hookIdentity, worktree string) error {
	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		file := pendingHookFile(id.credential, worktree, harness)
		if file == "" {
			return nil
		}

		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}
