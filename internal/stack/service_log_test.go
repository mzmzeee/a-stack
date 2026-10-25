package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceLog(t *testing.T) {
	dir := filepath.Join(privateTestDir(t), "state")
	f, err := OpenServiceLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("keep"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	f, err = OpenServiceLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(" appended")
	f.Close()
	path := filepath.Join(dir, "service.log")
	if b, err := os.ReadFile(path); err != nil || string(b) != "keep appended" {
		t.Fatal(string(b), err)
	}
	if err = os.Truncate(path, 1<<20+1); err != nil {
		t.Fatal(err)
	}
	f, err = OpenServiceLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatal(info, err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err = os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, path); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if f, err = OpenServiceLog(dir); err == nil {
		f.Close()
		t.Fatal("opened a linked log")
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "untouched" {
		t.Fatal(string(b), err)
	}
}

func TestWindowsServiceCommands(t *testing.T) {
	name := "a-stack-S-1-5-21-123"
	for verb, want := range map[string]string{
		"start":   "schtasks /Change /TN " + name + " /ENABLE\nschtasks /Run /TN " + name,
		"stop":    "schtasks /Change /TN " + name + " /DISABLE\nschtasks /End /TN " + name,
		"restart": "schtasks /End /TN " + name + "\nAfter the service stops: schtasks /Run /TN " + name,
	} {
		if got := windowsServiceCommand(verb, name); got != want {
			t.Errorf("%s: %q", verb, got)
		}
	}
}

func TestServiceLogConcurrentWritersAppend(t *testing.T) {
	dir := privateTestDir(t)
	first, err := OpenServiceLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenServiceLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err = first.WriteString("first\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = second.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "service.log")); err != nil || string(data) != "first\nsecond\n" {
		t.Fatal(string(data), err)
	}
}
