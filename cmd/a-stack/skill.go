package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
	"github.com/mzmzeee/a-stack/skills"
)

const skillName = "a-stack"

func isSkillCommand(command string) bool {
	return command == "skill install" || command == "skill uninstall"
}

// skillFolders returns the a-stack skill folders for Claude Code and for the
// harnesses that read .agents/skills, such as Codex and pi. Without project
// they are in the home folder, and CLAUDE_CONFIG_DIR moves the Claude one;
// with project they are in the current folder.
func skillFolders(project bool) ([]string, error) {
	if project {
		dir, err := os.Getwd()
		if err != nil {
			return nil, invalid("cannot read the current folder")
		}
		return []string{filepath.Join(dir, ".claude", "skills", skillName), filepath.Join(dir, ".agents", "skills", skillName)}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, invalid("cannot find home directory")
	}
	claude := filepath.Join(home, ".claude")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			return nil, invalid("CLAUDE_CONFIG_DIR must be absolute")
		}
		claude = dir
	}
	return []string{filepath.Join(claude, "skills", skillName), filepath.Join(home, ".agents", "skills", skillName)}, nil
}

// skillCommand runs skill install or skill uninstall. It needs no service
// and no credential. With refresh, install replaces only the copies that
// exist, as update does after it installs a new binary.
func skillCommand(command string, project, refresh, machine bool, stdout io.Writer) error {
	folders, err := skillFolders(project)
	if err != nil {
		return err
	}
	done := []string{}
	for _, folder := range folders {
		found := true
		if command == "skill install" {
			if _, err = os.Lstat(folder); refresh && err != nil {
				continue
			}
			err = installSkill(folder)
		} else {
			found, err = removeSkill(folder)
		}
		if err != nil {
			return &stack.Error{Code: "storage_unavailable", Message: "cannot write " + folder + ": " + err.Error()}
		}
		if found {
			done = append(done, folder)
		}
	}
	key, text := "installed", "Installed the a-stack skill "+buildVersion()+" in:\n"
	if command == "skill uninstall" {
		key, text = "removed", "Removed the a-stack skill from:\n"
	}
	if len(done) == 0 {
		text = "The a-stack skill is not installed in " + strings.Join(folders, " or ") + ".\n"
	}
	if machine {
		return json.NewEncoder(stdout).Encode(map[string]any{"data": map[string]any{"skill": skillName, "version": buildVersion(), key: done}})
	}
	for _, folder := range done {
		text += "  " + folder + "\n"
	}
	_, err = io.WriteString(stdout, text)
	return err
}

// installSkill writes the embedded skill to a temporary folder next to
// target and renames it into place, so that an older copy is replaced whole
// and no removed file stays behind. Other skills are not touched.
func installSkill(target string) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+skillName+"-new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	err = fs.WalkDir(skills.FS, skillName, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path := filepath.Join(tmp, filepath.FromSlash(strings.TrimPrefix(name, skillName)))
		if d.IsDir() {
			if err = os.MkdirAll(path, 0o755); err == nil {
				err = os.Chmod(path, 0o755) // MkdirTemp made tmp 0700
			}
			return err
		}
		data, err := skills.FS.ReadFile(name)
		if err == nil {
			err = os.WriteFile(path, data, 0o644)
		}
		if err == nil {
			err = os.Chmod(path, 0o644) // whatever the umask
		}
		return err
	})
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err != nil {
		return os.Rename(tmp, target)
	}
	old := tmp + "-old"
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Rename(old, target)
		return err
	}
	return os.RemoveAll(old)
}

// removeSkill deletes the a-stack skill folder and reports whether it was there.
func removeSkill(target string) (bool, error) {
	if _, err := os.Lstat(target); err != nil {
		return false, nil
	}
	return true, os.RemoveAll(target)
}
