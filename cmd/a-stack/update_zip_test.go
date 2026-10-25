package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows parts of update, tested on every system.

func zipArchive(t *testing.T, files ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < len(files); i += 2 {
		w, err := zw.Create(files[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(files[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if zw.Close() != nil {
		t.Fatal("cannot write zip")
	}
	return buf.Bytes()
}

func TestWindowsReleaseArchive(t *testing.T) {
	if got := releaseArchive("windows", "arm64"); got != "a-stack_windows_arm64.zip" {
		t.Fatal(got)
	}
	if got := releaseArchive("linux", "amd64"); got != "a-stack_linux_amd64.tar.gz" {
		t.Fatal(got)
	}
	binary, err := extractBinary(zipArchive(t, "README.md", "x", "a-stack.exe", "MZ new"), "windows")
	if err != nil || string(binary) != "MZ new" {
		t.Fatalf("%q %v", binary, err)
	}
	for name, archive := range map[string][]byte{
		"not a zip": tarGz(t, map[string]string{"a-stack.exe": "x"}),
		"no binary": zipArchive(t, "a-stack", "x"),
		"two":       zipArchive(t, "a-stack.exe", "x", "./a-stack.exe", "y"),
		"a folder":  zipArchive(t, "a-stack.exe/", ""),
		"parent":    zipArchive(t, "a-stack.exe", "x", "../evil.exe", "x"),
		"backslash": zipArchive(t, "a-stack.exe", "x", `..\evil.exe`, "x"),
		"drive":     zipArchive(t, "a-stack.exe", "x", "C:/evil.exe", "x"),
		"absolute":  zipArchive(t, "/a-stack.exe", "x"),
	} {
		if _, err := extractBinary(archive, "windows"); err == nil {
			t.Errorf("%s: extracted", name)
		} else if unsafe := strings.Contains(err.Error(), "unsafe"); unsafe != (name == "parent" || name == "backslash" || name == "drive" || name == "absolute") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A Linux archive still takes a : in a name.
	if _, err := extractBinary(tarGz(t, map[string]string{"a-stack": "x", "a:b": "y"}), "linux"); err != nil {
		t.Fatal(err)
	}
}

func TestPackageUpdate(t *testing.T) {
	for exe, want := range map[string]string{
		`C:\Users\me\scoop\apps\a-stack\0.1.6\a-stack.exe`:                                          "scoop update a-stack",
		`D:\Scoop\Apps\a-stack\current\a-stack.exe`:                                                 "scoop update a-stack",
		`C:\Users\me\AppData\Local\Microsoft\WinGet\Packages\mzmzeee.a-stack_Microsoft\a-stack.exe`: "winget upgrade a-stack",
		`C:\Users\me\AppData\Local\Programs\a-stack\a-stack.exe`:                                    "",
		"/home/me/.local/bin/a-stack":                                                               "",
	} {
		if got := packageUpdate(exe); got != want {
			t.Errorf("packageUpdate(%q) = %q, want %q", exe, got, want)
		}
	}
}

// TestReplaceBinaryOnWindows runs the Windows steps on this system: the
// running binary moves aside to .old, and back when the new one cannot
// take its place.
func TestReplaceBinaryOnWindows(t *testing.T) {
	dir := t.TempDir()
	exe, tmp := filepath.Join(dir, "a-stack.exe"), filepath.Join(dir, ".a-stack-update-1.exe")
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string { data, _ := os.ReadFile(path); return string(data) }
	write(exe, "v1")
	write(tmp, "v2")
	if err := replaceBinary(tmp, exe, true); err != nil || read(exe) != "v2" || read(exe+".old") != "v1" {
		t.Fatalf("first update: %v, exe %q, old %q", err, read(exe), read(exe+".old"))
	}
	write(tmp, "v3")
	if err := replaceBinary(tmp, exe, true); err != nil || read(exe) != "v3" || read(exe+".old") != "v2" {
		t.Fatalf("second update: %v, exe %q, old %q", err, read(exe), read(exe+".old"))
	}
	// The new binary is gone: the old one comes back.
	if err := replaceBinary(filepath.Join(dir, "missing.exe"), exe, true); err == nil || read(exe) != "v3" {
		t.Fatalf("failed update: %v, exe %q", err, read(exe))
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("the temporary file stays")
	}
}

func TestPackageUpdateCustomScoopRoot(t *testing.T) {
	t.Setenv("SCOOP", `D:\tools`)
	t.Setenv("SCOOP_GLOBAL", `D:\shared`)
	for _, exe := range []string{`D:\tools\apps\a-stack\current\a-stack.exe`, `d:/SHARED/apps/a-stack/1.0/a-stack.exe`} {
		if got := packageUpdate(exe); got != "scoop update a-stack" {
			t.Fatal(exe, got)
		}
	}
	if got := packageUpdate(`D:\tools-other\apps\a-stack\current\a-stack.exe`); got != "" {
		t.Fatal(got)
	}
}
