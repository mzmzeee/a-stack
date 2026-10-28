package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mzmzeee/a-stack/skills"
)

// Known contents from releases with skill install (v0.1.4 onward) and hook
// install's shims. Keep earlier hashes when adding a release's hashes. A
// header or a matching filename alone is not evidence that a file is ours.
var installedHashes = map[string][]string{
	"a-stack/SKILL.md": {
		"53b9655db7cdd9936c14f94e246e00d6f3c1ddeda09e00a531b09638e67afb6b",
		"196de764c002295babe1481128d98118713516c779f0dae0e50d0c148608c77e",
		"4cb13e86d41fc5bea17af4ae8875b2066ff2a40ea53946801d5d5a3ae7a338b3",
		"f8c2f623096c7535da88b3a48c31eae8cfa97da01e944cee75b22d11ae8584b2",
		"fb5c304cd5d9e1d39a5ea43eaa6806d67dfd63ea5e7c82146883cb986c3c2a8e",
	},
	"a-stack/references/commands.md": {
		"7e39a943254c75f532851334d84ab0dced7d3b4669c63453acc82dbacd5944d1",
		"27bd06fb857831056420dbd15540fd9871f4cb803b9cf92d347434ac0b9905ee",
		"2d54b3a97b0c1af04659f07f8ef203df9c1ba6841b3487b93768ae3c5aff928f",
		"836b537dcf2e1eee3d0d4dfc3345a11b24e1a357636b6ae9dad3fc50616d46ac",
	},
	"a-stack/references/orchestrator.md": {
		"1028c12ff992947f722189640fe4f8454a307e41b12d3c20f125d7bc1253b007",
		"29f27be25417eaba465287b85c0cf1ce47a8bed1725a600a5571e93607bea137",
		"adc7235cb98612dc9ad4b7e7a6d50f8f7baa0b95d5470b1b9588adb2117e37e0",
		"e3a9a0a5de8ad75c510fa884fdf573a6440790f9a0f731dccbec709d83b61530",
		"1f68fb3899c51b82294c78a49eb6acc5b842f9a8c01913e26e2f321654a53be4",
	},
	"pi":       {"2ef767cd4b92bc018c955a8a8c90ed6dd09523ae3698855b9dea2e91d3d71b76"},
	"opencode": {"ecb0df1a4255d62bd89c125a0de4a77b3ac1fa8781489c6389fd6d0e36d7500a"},
}

// refreshFile replaces only an unchanged, known installed file. Missing,
// edited, foreign and linked files stay untouched, as do extra user files.
func refreshFile(file, name string, content []byte) (bool, error) {
	info, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return false, nil
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	old, err := os.ReadFile(file)
	if err != nil {
		return false, err
	}
	if bytes.Equal(old, content) {
		return false, nil
	}
	sum := sha256.Sum256(old)
	if !slices.Contains(installedHashes[name], hex.EncodeToString(sum[:])) {
		return false, nil
	}
	if err := writeAtomic(file, content, info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

func refreshSkill(folder string) ([]string, error) {
	files := []string{}
	err := fs.WalkDir(skills.FS, skillName, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := skills.FS.ReadFile(name)
		if err != nil {
			return err
		}
		file := filepath.Join(folder, filepath.FromSlash(strings.TrimPrefix(name, skillName+"/")))
		changed, err := refreshFile(file, name, content)
		if changed {
			files = append(files, file)
		}
		return err
	})
	return files, err
}

// refreshInstalledFiles is run by the replacement binary, whose embedded
// files are the versions we need. Older binaries refuse update --refresh,
// rather than running an older, destructive skill install --refresh.
func refreshInstalledFiles(machine bool, stdout io.Writer) error {
	files, folders, targets := []string{}, []string{}, []string{}
	for _, project := range []bool{false, true} {
		paths, err := skillFolders(project)
		if err != nil {
			return err
		}
		targets = append(targets, paths...)
	}
	var settings map[string]string
	if shimsSupported {
		var err error
		settings, err = hookSettings(hookFlags{pi: true, opencode: true}, false)
		if err != nil {
			return err
		}
	}
	var refreshErr error
	for _, folder := range targets {
		changed, err := refreshSkill(folder)
		refreshErr = errors.Join(refreshErr, err)
		if len(changed) > 0 {
			folders = append(folders, folder)
			files = append(files, changed...)
		}
	}
	if shimsSupported {
		for _, harness := range []string{"pi", "opencode"} {
			shim := shims[harness]
			file := filepath.Join(filepath.Dir(settings[harness]), shim.path)
			changed, err := refreshFile(file, harness, []byte(shim.content))
			refreshErr = errors.Join(refreshErr, err)
			if changed {
				files = append(files, file)
			}
		}
	}
	if machine {
		return errors.Join(refreshErr, json.NewEncoder(stdout).Encode(map[string]any{"data": map[string]any{"skills": folders, "refreshed_files": files}}))
	}
	for _, file := range files {
		if _, err := io.WriteString(stdout, "Refreshed "+file+"\n"); err != nil {
			return err
		}
	}
	return refreshErr
}
