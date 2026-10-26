package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/skills"
)

func TestRefreshInstalledFiles(t *testing.T) {
	oldSkill, err := os.ReadFile("testdata/refresh/skill-v0.1.4.md")
	if err != nil {
		t.Fatal(err)
	}
	home := hookHome(t)
	withShims(t, true)
	t.Chdir(t.TempDir())
	// Resolve platform temp symlinks; intentionally linked install paths are
	// skipped, but these fixture folders are normal directories.
	home, _ = filepath.EvalSymlinks(home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "custom-claude"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "custom-pi"))
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(home, "custom-opencode"))
	var expected []string
	write := func(file string, content []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, project := range []bool{false, true} {
		folders, err := skillFolders(project)
		if err != nil {
			t.Fatal(err)
		}
		for _, folder := range folders {
			file := filepath.Join(folder, "SKILL.md")
			write(file, oldSkill)
			expected = append(expected, file)
			// A user-added file and an edited reference must survive, and
			// missing references must not be created behind the user's back.
			write(filepath.Join(folder, "mine.md"), []byte("mine"))
			write(filepath.Join(folder, "references", "commands.md"), []byte("my edited commands"))
		}
	}
	settings, err := hookSettings(hookFlags{pi: true, opencode: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"pi", "opencode"} {
		old := shims[harness]
		h := harness
		t.Cleanup(func() { shims[h] = old })
		file := filepath.Join(filepath.Dir(settings[harness]), old.path)
		write(file, []byte(old.content))
		write(settings[harness], []byte("{\"user_setting\":true}\n"))
		expected = append(expected, file)
		newShim := old
		newShim.content += "\n// new version\n"
		shims[harness] = newShim
	}
	var out, errout bytes.Buffer
	if code := run([]string{"update", "--refresh", "--json"}, &out, &errout); code != 0 {
		t.Fatalf("exit %d: %s", code, errout.String())
	}
	var result struct {
		Data struct {
			Skills []string
			Files  []string `json:"refreshed_files"`
		}
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || !slices.Equal(result.Data.Files, expected) || len(result.Data.Skills) != 4 {
		t.Fatalf("%s %v", out.String(), err)
	}
	for _, file := range expected {
		got, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(file, "SKILL.md") {
			want, _ := skills.FS.ReadFile("a-stack/SKILL.md")
			if !bytes.Equal(got, want) {
				t.Fatalf("not refreshed: %s", file)
			}
			folder := filepath.Dir(file)
			if data, _ := os.ReadFile(filepath.Join(folder, "mine.md")); string(data) != "mine" {
				t.Fatal("extra file changed")
			}
			if data, _ := os.ReadFile(filepath.Join(folder, "references", "commands.md")); string(data) != "my edited commands" {
				t.Fatal("edited file changed")
			}
			if _, err := os.Stat(filepath.Join(folder, "references", "orchestrator.md")); !os.IsNotExist(err) {
				t.Fatal("created missing reference")
			}
		} else if !strings.HasSuffix(string(got), "// new version\n") {
			t.Fatalf("not refreshed: %s", file)
		}
	}
	for _, file := range settings {
		if data, _ := os.ReadFile(file); string(data) != "{\"user_setting\":true}\n" {
			t.Fatal("hook configuration changed")
		}
	}
	// No changes on the second refresh.
	out.Reset()
	errout.Reset()
	if code := run([]string{"update", "--refresh"}, &out, &errout); code != 0 || out.Len() != 0 || errout.Len() != 0 {
		t.Fatalf("second refresh: %d %s %s", code, out.String(), errout.String())
	}
}

func TestRefreshPreservesEditedForeignAndLinkedFiles(t *testing.T) {
	old, err := os.ReadFile("testdata/refresh/skill-v0.1.4.md")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(old)
	if !slices.Contains(installedHashes["a-stack/SKILL.md"], hex.EncodeToString(sum[:])) {
		t.Fatal("fixture is not a known released skill")
	}
	for _, body := range [][]byte{append(bytes.Clone(old), []byte("\nmy edit\n")...), []byte("not ours"), []byte(shims["pi"].header + "\nmy extension\n")} {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, body, 0o640); err != nil {
			t.Fatal(err)
		}
		if changed, err := refreshFile(file, "a-stack/SKILL.md", []byte("new")); changed || err != nil {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if data, _ := os.ReadFile(file); !bytes.Equal(data, body) {
			t.Fatal("user file changed")
		}
	}
	t.Run("links", func(t *testing.T) {
		unixOnly(t, "symlink support")
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, old, 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if changed, err := refreshFile(link, "a-stack/SKILL.md", []byte("new")); changed || err != nil {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		linkedDir := filepath.Join(t.TempDir(), "linked-dir")
		if err := os.Symlink(dir, linkedDir); err != nil {
			t.Fatal(err)
		}
		if changed, err := refreshFile(filepath.Join(linkedDir, "target"), "a-stack/SKILL.md", []byte("new")); changed || err != nil {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if data, _ := os.ReadFile(target); !bytes.Equal(data, old) {
			t.Fatal("linked target changed")
		}
	})
}

func TestUpdateNoRefreshAndUnsupportedRefresh(t *testing.T) {
	for _, mode := range []string{"no-refresh", "old-binary", "old-binary-json", "files"} {
		t.Run(mode, func(t *testing.T) {
			binary := fakeBinary("v0.2.0")
			if strings.HasPrefix(mode, "old-binary") {
				binary = "#!/bin/sh\nif [ \"$1\" != version ]; then exit 2; fi\necho '{\"data\":{\"version\":\"v0.2.0\"}}'\n"
			}
			release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": binary})}}
			_, log := updateSetup(t, "v0.1.0", release)
			t.Setenv("A_STACK_TEST_REFRESH", "1")
			var args []string
			if mode == "no-refresh" {
				args = append(args, "--no-refresh")
			}
			if mode == "old-binary-json" {
				args = append(args, "--json")
			}
			out, _ := runUpdate(t, 0, args...)
			if mode == "no-refresh" {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatal("ran refresh despite --no-refresh")
				}
			}
			if strings.HasPrefix(mode, "old-binary") && !strings.Contains(out, "could not safely refresh") {
				t.Fatal(out)
			}
			if mode == "files" && (!strings.Contains(out, "Refreshed /skills/a-stack/SKILL.md\n") || !strings.Contains(out, "Refreshed /pi/a-stack.ts\n")) {
				t.Fatal(out)
			}
		})
	}
}
