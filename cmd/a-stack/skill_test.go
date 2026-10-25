package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/skills"
)

// skillHome gives the test an empty home folder and no CLAUDE_CONFIG_DIR.
func skillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func runSkill(t *testing.T, args ...string) string {
	t.Helper()
	var out, errout bytes.Buffer
	if code := run(args, &out, &errout); code != 0 || errout.Len() != 0 {
		t.Fatalf("%v: exit %d stderr=%s", args, code, errout.String())
	}
	return out.String()
}

// treeFiles maps each file below root to its content and mode, and each
// folder to its mode.
func treeFiles(t *testing.T, fsys fs.FS) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			files[name+"/"] = ""
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		files[name] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func checkInstalled(t *testing.T, folder string) {
	t.Helper()
	embedded, _ := fs.Sub(skills.FS, "a-stack")
	if got, want := treeFiles(t, os.DirFS(folder)), treeFiles(t, embedded); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s differs from the embedded skill:\n%v\n%v", folder, got, want)
	}
	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		want := fs.FileMode(0o644)
		if d.IsDir() {
			want = fs.ModeDir | 0o755
		}
		if err == nil && info.Mode() != want {
			t.Fatalf("%s has mode %v, want %v", path, info.Mode(), want)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSkillEmbedMatchesRepository(t *testing.T) {
	embedded, _ := fs.Sub(skills.FS, "a-stack")
	disk := treeFiles(t, os.DirFS(filepath.Join("..", "..", "skills", "a-stack")))
	if !reflect.DeepEqual(treeFiles(t, embedded), disk) || disk["SKILL.md"] == "" {
		t.Fatal("the embedded skill differs from skills/a-stack; a file is not embedded")
	}
}

func TestSkillInstallReplacesOnlyTheSkill(t *testing.T) {
	home := skillHome(t)
	claude := filepath.Join(home, ".claude", "skills", "a-stack")
	agents := filepath.Join(home, ".agents", "skills", "a-stack")
	out := runSkill(t, "skill", "install")
	if out != "Installed the a-stack skill dev in:\n  "+claude+"\n  "+agents+"\n" {
		t.Fatalf("output %q", out)
	}
	checkInstalled(t, claude)
	checkInstalled(t, agents)

	// An older copy loses its stale files; other skills stay.
	other := filepath.Join(home, ".claude", "skills", "other", "SKILL.md")
	for _, path := range []string{filepath.Join(claude, "references", "stale.md"), filepath.Join(agents, "stale.md"), other} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(claude, "SKILL.md"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSkill(t, "skill", "install")
	checkInstalled(t, claude)
	checkInstalled(t, agents)
	if data, err := os.ReadFile(other); err != nil || string(data) != "old" {
		t.Fatalf("other skill changed: %q %v", data, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(claude))
	if len(entries) != 2 {
		t.Fatalf("temporary folders stay behind: %v", entries)
	}

	if out := runSkill(t, "skill", "uninstall"); out != "Removed the a-stack skill from:\n  "+claude+"\n  "+agents+"\n" {
		t.Fatalf("uninstall output %q", out)
	}
	for _, path := range []string{claude, agents} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s stays: %v", path, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal(err)
	}
	if out := runSkill(t, "skill", "uninstall"); !strings.HasPrefix(out, "The a-stack skill is not installed in ") {
		t.Fatalf("second uninstall %q", out)
	}
}

func TestSkillInstallProjectAndJSON(t *testing.T) {
	home := skillHome(t)
	project := t.TempDir()
	t.Chdir(project)
	var result struct {
		Data struct {
			Skill     string   `json:"skill"`
			Version   string   `json:"version"`
			Installed []string `json:"installed"`
		} `json:"data"`
	}
	out := runSkill(t, "skill", "install", "--project", "--json")
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := []string{filepath.Join(project, ".claude", "skills", "a-stack"), filepath.Join(project, ".agents", "skills", "a-stack")}
	if result.Data.Skill != "a-stack" || result.Data.Version != "dev" || !reflect.DeepEqual(result.Data.Installed, want) {
		t.Fatalf("json %s", out)
	}
	for _, folder := range want {
		checkInstalled(t, folder)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("--project wrote to the home folder: %v", entries)
	}
	out = runSkill(t, "skill", "uninstall", "--project", "--json")
	if !strings.Contains(out, `"removed":["`+want[0]+`","`+want[1]+`"]`) {
		t.Fatalf("uninstall json %s", out)
	}
}

func TestSkillInstallRefreshWritesOnlyExistingCopies(t *testing.T) {
	home := skillHome(t)
	claude := filepath.Join(home, ".claude", "skills", "a-stack")
	if out := runSkill(t, "skill", "install", "--refresh"); !strings.HasPrefix(out, "The a-stack skill is not installed in ") {
		t.Fatalf("output %q", out)
	}
	if err := os.MkdirAll(filepath.Join(claude, "old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out := runSkill(t, "skill", "install", "--refresh", "--json"); !strings.Contains(out, `"installed":["`+claude+`"]`) {
		t.Fatalf("output %q", out)
	}
	checkInstalled(t, claude)
	if _, err := os.Stat(filepath.Join(home, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("--refresh wrote a new copy: %v", err)
	}
}

func TestSkillInstallClaudeConfigDirAndFlags(t *testing.T) {
	home := skillHome(t)
	config := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	runSkill(t, "skill", "install")
	checkInstalled(t, filepath.Join(config, "skills", "a-stack"))
	checkInstalled(t, filepath.Join(home, ".agents", "skills", "a-stack"))
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude written although CLAUDE_CONFIG_DIR is set: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "relative")
	for _, args := range [][]string{{"skill", "install"}, {"skill", "install", "--state-dir", "/x"}, {"skill", "install", "--as", "agent"}, {"skill", "--project"}, {"new", "--project"}} {
		var out, errout bytes.Buffer
		if run(args, &out, &errout) != 2 || out.Len() != 0 || !strings.Contains(errout.String(), "invalid_input") {
			t.Fatalf("%v: want invalid_input, got %s", args, errout.String())
		}
	}
}
