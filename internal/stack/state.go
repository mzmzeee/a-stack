// Package stack implements the local service foundation. It runs on Linux,
// macOS and Windows; its state directory lock is held until the database
// closes.
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
	"runtime"
	"strconv"
	"strings"
	"time"
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
	root, err := defaultStateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "a-stack"), nil
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
			return "", storageError(err)
		}
		next := filepath.Dir(probe)
		if next == probe {
			return "", storageError()
		}
		probe = next
	}
	return path, nil
}
func privateRead(path string) ([]byte, error) {
	f, err := openPrivate(path, false)
	if err != nil {
		return nil, failure("invalid_input", "private file is missing or inaccessible")
	}
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
		err = Rename(tmp, path)
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

// Rename renames from to to, also over a file. On Windows that fails while
// another process has the target open without delete sharing, as Go opens
// files; Rename then tries again for up to a second.
func Rename(from, to string) error {
	err := os.Rename(from, to)
	for i := 0; i < 20 && renameBusy(err); i++ {
		time.Sleep(50 * time.Millisecond)
		err = os.Rename(from, to)
	}
	return err
}

// ReplacePrivate replaces path with v as JSON, mode 0600: a synced temporary
// file is renamed over it. Of two writers at once, the last one wins.
func ReplacePrivate(path string, v any) error { return writePrivate(path, v, true) }

type stateLock struct {
	dir  string
	file *os.File
}

func lockState(path string) (*stateLock, error) {
	dir, err := statePath(path)
	if err != nil {
		return nil, err
	}
	if err = MkdirPrivate(dir); err != nil {
		return nil, storageError(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, storageError(err)
	}
	if err = checkPrivate(info, true); err != nil {
		return nil, err
	}
	f, err := lockFile(filepath.Join(dir, "service.lock"))
	if err != nil {
		return nil, err
	}
	return &stateLock{dir: dir, file: f}, nil
}

// ErrStateOwned means another serve holds the state directory lock. The CLI
// gives it its own exit code so that systemd does not restart in a loop.
var ErrStateOwned = failure("conflict", "another a-stack serve is already running for this state directory; stop it (Ctrl-C in its terminal, or "+ServiceCommand("stop")+") or use it")

// LaunchdLabel names the launchd agent that a-stack service install writes
// on macOS.
const LaunchdLabel = "dev.a-stack"

// ServiceCommand is the shell command that does verb (start, stop or
// restart) to the service that a-stack service install set up: the systemd
// user unit on Linux, the launchd agent on macOS, the Task Scheduler task on
// Windows. On Windows, the task starts a stopped serve again within a
// minute; restart is two commands for PowerShell. On macOS, stop sends
// SIGTERM; serve then exits with 0, and launchd starts it again only after
// a failure.
func ServiceCommand(verb string) string {
	if runtime.GOOS == "windows" {
		return map[string]string{"start": "schtasks /Run /TN a-stack", "stop": "schtasks /End /TN a-stack", "restart": "schtasks /End /TN a-stack; schtasks /Run /TN a-stack"}[verb]
	}
	if runtime.GOOS == "darwin" {
		action := map[string]string{"start": "kickstart", "stop": "kill TERM", "restart": "kickstart -k"}[verb]
		return "launchctl " + action + " gui/" + strconv.Itoa(os.Getuid()) + "/" + LaunchdLabel
	}
	return "systemctl --user " + verb + " a-stack"
}

func (l *stateLock) close() { unlockFile(l.file) }

// Credential is a private credential file. An Agent credential also names its
// Workspace, Session and Agent; these fields are not secret and give no access.
// Files from before v0.1.5 have none of them.
type Credential struct {
	Kind        CapabilityKind `json:"kind"`
	Token       string         `json:"token"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	AgentName   string         `json:"agent_name,omitempty"`
	Enrollment  *Enrollment    `json:"enrollment,omitempty"`
}

// ValidID reports whether id has the form of a Workspace, Session, Agent,
// Task or reference ID.
func ValidID(id string) bool { return validToken(id) }
func validToken(token string) bool {
	b, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == token
}
func validContext(c Credential) bool {
	return (c.WorkspaceID == "" || validToken(c.WorkspaceID)) && (c.SessionID == "" || validToken(c.SessionID)) && (c.AgentName == "" || agentName.MatchString(c.AgentName))
}
func ReadCredential(path string) (Credential, error) {
	data, err := privateRead(path)
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if err = decodeJSON(data, &c); err != nil || !validToken(c.Token) || !validKind(c.Kind) || !validContext(c) {
		return Credential{}, failure("invalid_input", "invalid credential file")
	}
	return c, nil
}

// An Agent never starts the service, so the hint also tells it whom to ask.
var (
	startHint          = "start it with a-stack serve (or " + ServiceCommand("start") + "), or ask the Operator to start it; then run the same command again"
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
	f, err := openPrivate(path, true)
	if err != nil {
		return "", storageError(err)
	}
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
