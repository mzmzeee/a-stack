package stack

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var (
	procGetNamedSecurityInfoW = advapi32.NewProc("GetNamedSecurityInfoW")
	procSDToString            = advapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
)

// daclOf reads back the access list of path as SDDL.
func daclOf(t *testing.T, path string) string {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	const fileObject, daclInformation = 1, 4
	var descriptor uintptr
	if r, _, _ := procGetNamedSecurityInfoW.Call(uintptr(unsafe.Pointer(p)), fileObject, daclInformation, 0, 0, 0, 0, uintptr(unsafe.Pointer(&descriptor))); r != 0 {
		t.Fatalf("GetNamedSecurityInfoW %s: error %d", path, r)
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	var text *uint16
	var size uint32
	if r, _, e := procSDToString.Call(descriptor, 1, daclInformation, uintptr(unsafe.Pointer(&text)), uintptr(unsafe.Pointer(&size))); r == 0 {
		t.Fatal("ConvertSecurityDescriptorToStringSecurityDescriptorW:", e)
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(text))))
	return syscall.UTF16ToString(unsafe.Slice(text, size))
}

func currentSID(t *testing.T) string {
	t.Helper()
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// TestMkdirPrivateACL reads back the access list of a new state folder and
// of a credential file written in it: only SYSTEM, the Administrators and
// this user, and nothing inherited from the folder above.
func TestMkdirPrivateACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "state")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal("a second MkdirPrivate:", err)
	}
	sid := currentSID(t)
	if got := daclOf(t, dir); got != privateSDDL(sid) {
		t.Fatalf("state folder access list %s, want %s", got, privateSDDL(sid))
	}
	file := filepath.Join(dir, "operator.json")
	if err := writePrivate(file, Credential{Kind: OperatorCapability, Token: opaqueID()}, false); err != nil {
		t.Fatal(err)
	}
	got := daclOf(t, file)
	aces := regexp.MustCompile(`\(([^)]*)\)`).FindAllStringSubmatch(got, -1)
	if len(aces) != 3 {
		t.Fatalf("credential file access list %s, want 3 entries", got)
	}
	for _, ace := range aces {
		fields := strings.Split(ace[1], ";")
		if trustee := fields[len(fields)-1]; fields[0] != "A" || fields[2] != "FA" || trustee != "SY" && trustee != "BA" && trustee != sid {
			t.Fatalf("credential file access list %s has the entry %s", got, ace[0])
		}
	}
	if _, err := ReadCredential(file); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultStateDirIsInLocalAppData(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("LOCALAPPDATA", `C:\Users\me\AppData\Local`)
	if dir, err := DefaultStateDir(); err != nil || dir != `C:\Users\me\AppData\Local\a-stack` {
		t.Fatal(dir, err)
	}
	t.Setenv("XDG_STATE_HOME", `D:\state`)
	if dir, err := DefaultStateDir(); err != nil || dir != `D:\state\a-stack` {
		t.Fatal(dir, err)
	}
}

// TestRenameWaitsForReader: a reader that Go opened, without delete
// sharing, blocks a rename over its file until it closes.
func TestRenameWaitsForReader(t *testing.T) {
	dir := t.TempDir()
	target, tmp := filepath.Join(dir, "service.json"), filepath.Join(dir, "new")
	if os.WriteFile(target, []byte("old"), 0600) != nil || os.WriteFile(tmp, []byte("new"), 0600) != nil {
		t.Fatal("write")
	}
	reader, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if !renameBusy(os.Rename(tmp, target)) {
		t.Skip("this Windows renames over an open file")
	}
	go func() { time.Sleep(200 * time.Millisecond); reader.Close() }()
	if err = Rename(tmp, target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Fatalf("target holds %q", data)
	}
}

// TestPrivateReadSharesDelete: a writer can replace a private file while
// a reader has it open.
func TestPrivateReadSharesDelete(t *testing.T) {
	dir := privateTestDir(t)
	file := filepath.Join(dir, "service.json")
	if err := writePrivate(file, Discovery{URL: "http://127.0.0.1:1"}, true); err != nil {
		t.Fatal(err)
	}
	f, err := openPrivate(file, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = os.Rename(file, file+".moved"); err != nil {
		t.Fatal("a private read blocks a rename:", err)
	}
}

// TestPrivateReadSkipsLinks: a link to a credential file is not read.
func TestPrivateReadSkipsLinks(t *testing.T) {
	dir := privateTestDir(t)
	file := filepath.Join(dir, "operator.json")
	if err := writePrivate(file, Credential{Kind: OperatorCapability, Token: opaqueID()}, false); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Skip("cannot make a symlink here:", err)
	}
	if _, err := privateRead(link); err == nil {
		t.Fatal("privateRead followed a link")
	}
}
