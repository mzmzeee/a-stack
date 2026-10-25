// Package stack implements the local service foundation. Linux is the initial
// supported platform; its advisory flock is held until the database closes.
package stack

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxPrivateFile = 4096

func opaqueID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("operating system random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func DefaultStateDir() (string, error) {
	if root := os.Getenv("XDG_STATE_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", failure("invalid_input", "XDG_STATE_HOME must be absolute")
		}
		return filepath.Join(root, "a-stack"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", failure("invalid_input", "cannot find home directory")
	}
	return filepath.Join(home, ".local", "state", "a-stack"), nil
}
func statePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", failure("invalid_input", "state directory must be absolute")
	}
	path = filepath.Clean(path)
	// Resolve existing ancestors too: a symlink must not bypass the checkout guard.
	probe := path
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for p := resolved; ; p = filepath.Dir(p) {
				if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
					return "", failure("invalid_input", "application state and generated credentials must be outside project checkouts")
				}
				if filepath.Dir(p) == p {
					break
				}
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", storageError()
		}
		next := filepath.Dir(probe)
		if next == probe {
			return "", storageError()
		}
		probe = next
	}
	return path, nil
}
func checkPrivate(info os.FileInfo, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return failure("invalid_input", "state and credential paths must be owned by this user with owner-only permissions and no symlinks")
	}
	return nil
}
func privateRead(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, failure("invalid_input", "private file is missing or inaccessible")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, storageError(err)
	}
	if err = checkPrivate(info, false); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPrivateFile+1))
	if err != nil {
		return nil, storageError(err)
	}
	if len(data) > maxPrivateFile {
		return nil, failure("invalid_input", "private file is too large")
	}
	return data, nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return storageError(err)
	}
	defer f.Close()
	if f.Sync() != nil {
		return storageError()
	}
	return nil
}
func writePrivate(path string, v any, replace bool) error {
	b, err := json.Marshal(v)
	if err != nil {
		return storageError(err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".a-stack-*")
	if err != nil {
		return storageError(err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return storageError()
	}
	if replace {
		err = os.Rename(tmp, path)
	} else {
		err = os.Link(tmp, path)
	}
	if errors.Is(err, os.ErrExist) {
		return failure("conflict", "private file already exists")
	}
	if err != nil {
		return storageError(err)
	}
	return syncDir(filepath.Dir(path))
}

type stateLock struct {
	dir  string
	file *os.File
}

func lockState(path string) (*stateLock, error) {
	dir, err := statePath(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, storageError(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, storageError(err)
	}
	if err = checkPrivate(info, true); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(dir, "service.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, storageError(err)
	}
	f := os.NewFile(uintptr(fd), "service.lock")
	info, err = f.Stat()
	if err == nil {
		err = checkPrivate(info, false)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrStateOwned
		}
		return nil, storageError()
	}
	return &stateLock{dir: dir, file: f}, nil
}

// ErrStateOwned means another serve holds the state directory lock. The CLI
// gives it its own exit code so that systemd does not restart in a loop.
var ErrStateOwned = failure("conflict", "another a-stack serve is already running for this state directory; stop it (Ctrl-C in its terminal, or systemctl --user stop a-stack) or use it")

func (l *stateLock) close() { _ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); _ = l.file.Close() }

type Credential struct {
	Kind       CapabilityKind `json:"kind"`
	Token      string         `json:"token"`
	Enrollment *Enrollment    `json:"enrollment,omitempty"`
}

func validToken(token string) bool {
	b, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == token
}
func ReadCredential(path string) (Credential, error) {
	data, err := privateRead(path)
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if err = decodeJSON(data, &c); err != nil || !validToken(c.Token) || !validKind(c.Kind) {
		return Credential{}, failure("invalid_input", "invalid credential file")
	}
	return c, nil
}

// An Agent never starts the service, so the hint also tells it whom to ask.
const (
	startHint          = "start it with a-stack serve (or systemctl --user start a-stack), or ask the Operator to start it; then run the same command again"
	serviceDown        = "the a-stack service is not running; " + startHint
	serviceUnreachable = "cannot reach the a-stack service; if it stopped, " + startHint
)

// unreachable is retryable: a request that failed may still have committed,
// and the same command with the same operation ID returns its receipt.
func unreachable(message string) *Error {
	return &Error{Code: "storage_unavailable", Message: message, Retryable: true}
}

type Discovery struct {
	URL        string `json:"url"`
	DatabaseID string `json:"database_id"`
	InstanceID string `json:"instance_id"`
}

func ReadDiscovery(dir string) (Discovery, error) {
	var d Discovery
	data, err := privateRead(filepath.Join(dir, "service.json"))
	if err != nil {
		return d, unreachable(serviceDown)
	}
	if decodeJSON(data, &d) != nil || !validToken(d.DatabaseID) || !validToken(d.InstanceID) || !strings.HasPrefix(d.URL, "http://127.0.0.1:") {
		return d, failure("invalid_input", "invalid service discovery")
	}
	return d, nil
}
func ensureDatabaseFile(dir string) (string, error) {
	path := filepath.Join(dir, "state.db")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return "", storageError(err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", storageError(err)
	}
	if err = checkPrivate(info, false); err != nil {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err = os.Lstat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", storageError(err)
		}
		if err = checkPrivate(info, false); err != nil {
			return "", err
		}
	}
	return path, nil
}
func localURL(address string) string { return fmt.Sprintf("http://%s", address) }
