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
	return descriptorDACL(t, descriptor)
}

func descriptorDACL(t *testing.T, descriptor uintptr) string {
	t.Helper()
	const daclInformation = 4
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
	// Windows may render the current SID as an SDDL alias such as LA.
	// Normalize the expected trustees with the same native conversion.
	sddl, _ := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + sid + ")")
	var descriptor uintptr
	if r, _, e := procConvertStringSD.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0); r == 0 {
		t.Fatal(e)
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	acePattern := regexp.MustCompile(`\(([^)]*)\)`)
	expected := map[string]bool{}
	for _, ace := range acePattern.FindAllStringSubmatch(descriptorDACL(t, descriptor), -1) {
		fields := strings.Split(ace[1], ";")
		expected[fields[len(fields)-1]] = true
	}
	if got := daclOf(t, dir); !strings.Contains(strings.SplitN(got, "(", 2)[0], "P") {
		t.Fatalf("state folder DACL is not protected: %s", got)
	}
	file := filepath.Join(dir, "operator.json")
	if err := writePrivate(file, Credential{Kind: OperatorCapability, Token: opaqueID()}, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, file} {
		got := daclOf(t, path)
		aces := acePattern.FindAllStringSubmatch(got, -1)
		if len(aces) != 3 {
			t.Fatalf("credential file access list %s, want 3 entries", got)
		}
		for _, ace := range aces {
			fields := strings.Split(ace[1], ";")
			if trustee := fields[len(fields)-1]; fields[0] != "A" || fields[2] != "FA" || !expected[trustee] {
				t.Fatalf("credential file access list %s has the entry %s", got, ace[0])
			}
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

// Existing directories must be checked without silently changing their ACL.
func TestRejectSharedWindowsPaths(t *testing.T) {
	dir := privateTestDir(t)
	file := filepath.Join(dir, "credential.json")
	if err := os.WriteFile(file, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	setACL := func(path, text string) {
		t.Helper()
		sddl, _ := syscall.UTF16PtrFromString(text)
		var descriptor uintptr
		if r, _, e := procConvertStringSD.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0); r == 0 {
			t.Fatal(e)
		}
		defer syscall.LocalFree(syscall.Handle(descriptor))
		p, _ := syscall.UTF16PtrFromString(path)
		if r, _, e := advapi32.NewProc("SetFileSecurityW").Call(uintptr(unsafe.Pointer(p)), 0x80000004, descriptor); r == 0 {
			t.Fatal(e)
		}
	}
	setACL(file, "D:P(A;;FA;;;WD)")
	if _, err := privateRead(file); err == nil {
		t.Fatal("read a shared credential")
	}
	setACL(dir, "D:P(A;OICI;FA;;;WD)")
	if err := MkdirPrivate(dir); err == nil {
		t.Fatal("accepted a shared existing folder")
	}
	if err := checkPrivateDirectory(dir); err == nil {
		t.Fatal("accepted a shared credential/backup directory")
	}
	setACL(dir, privateSDDL(currentSID(t)))
	if err := checkPrivateDirectory(dir); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsLocalStatePaths(t *testing.T) {
	for _, path := range []string{`\\server\share\state`, `\\?\C:\state`, `\\.\C:\state`, `//server/share/state`, `C:state`, `\state`} {
		if _, err := statePath(path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
}

// Go treats deduplicated reparse files as regular and some reparse folders
// retain ModeDir. Privacy checks must use the native attribute as well.
type reparseFileInfo struct{ os.FileInfo }

func (reparseFileInfo) Sys() any {
	return &syscall.Win32FileAttributeData{FileAttributes: syscall.FILE_ATTRIBUTE_REPARSE_POINT}
}
func TestPrivateRejectsReparseAttributes(t *testing.T) {
	dir := privateTestDir(t)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPrivate(reparseFileInfo{info}, true); err == nil {
		t.Fatal("accepted a reparse directory")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPrivate(reparseFileInfo{info}, false); err == nil {
		t.Fatal("accepted a regular reparse file")
	}
}
