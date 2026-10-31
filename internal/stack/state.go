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

// EnsurePrivateDirectory creates a private folder or checks the existing one.
func EnsurePrivateDirectory(dir string) error {
	if _, err := statePath(dir); err != nil {
		return err
	}

	if err := MkdirPrivate(dir); err != nil {
		return err
	}

	return checkPrivateDirectory(dir)
}

// OpenServiceLog validates the directory and the opened file before truncating
// a large log. The Windows console uses this before the service starts.
func OpenServiceLog(dir string) (*os.File, error) {
	if _, err := statePath(dir); err != nil {
		return nil, err
	}

	if err := MkdirPrivate(dir); err != nil {
		return nil, err
	}

	if err := checkPrivateDirectory(dir); err != nil {
		return nil, err
	}

	f, err := openPrivate(filepath.Join(dir, "service.log"), true)
	if err != nil {
		return nil, err
	}

	info, err := f.Stat()
	if err == nil {
		err = checkPrivate(info, false)
	}

	if err == nil && info.Size() > 1<<20 {
		err = f.Truncate(0)
	}

	closeErr := f.Close()

	if err != nil {
		return nil, err
	}

	if closeErr != nil {
		return nil, closeErr
	}

	// Reopen with append-only writes so concurrent startup attempts cannot
	// overwrite each other's output. Both opens reject links and unsafe ACLs.
	f, err = openPrivateAppend(filepath.Join(dir, "service.log"))
	if err != nil {
		return nil, err
	}

	info, err = f.Stat()
	if err == nil {
		err = checkPrivate(info, false)
	}

	if err != nil {
		f.Close()
		return nil, err
	}

	return f, nil
}

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
	if !filepath.IsAbs(path) || !localStatePath(path) {
		return "", failure("invalid_input", "state directory must be an absolute local filesystem path")
	}

	path = filepath.Clean(path)
	// Resolve existing ancestors too: a symlink must not bypass the checkout guard.
	probe := path
	for {
		resolved, err := ResolvePath(probe)
		if err == nil {
			if !localStatePath(resolved) {
				return "", failure("invalid_input", "state directory must be on a local filesystem")
			}

			for p := resolved; ; p = filepath.Dir(p) {
				if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
					return "", failure("invalid_input", "application state and generated credentials must be outside project checkouts, and "+p+" is a Git checkout (it has .git); choose a folder outside it, for the state with --state-dir")
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
		return nil, failure("invalid_input", fmt.Sprintf(`private file "%s" is missing or inaccessible`, path))
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
		return failure("conflict", fmt.Sprintf(`private file already exists at "%s"; refusing to overwrite it`, path))
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

// Open opens path for reading. On Windows a file that another process is
// renaming over cannot be opened for a moment, so it retries, like Rename.
func Open(path string) (*os.File, error) {
	f, err := os.Open(path)
	for i := 0; i < 20 && renameBusy(err); i++ {
		time.Sleep(50 * time.Millisecond)
		f, err = os.Open(path)
	}

	return f, err
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

	if err = checkPrivateDirectory(dir); err != nil {
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
// minute; stop disables the task too, and restart gives instructions for both
// cmd.exe and PowerShell. On macOS, stop sends
// SIGTERM; serve then exits with 0, and launchd starts it again only after
// a failure.
func ServiceCommand(verb string) string {
	if runtime.GOOS == "windows" {
		name, err := WindowsTaskName()
		if err != nil {
			return "a-stack service status"
		}

		return windowsServiceCommand(verb, name)
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
	Remote      string         `json:"remote,omitempty"`
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
	return (c.Remote == "" || remoteName.MatchString(c.Remote)) && (c.WorkspaceID == "" || validToken(c.WorkspaceID)) && (c.SessionID == "" || validToken(c.SessionID)) && (c.AgentName == "" || agentName.MatchString(c.AgentName))
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

func serviceHint(message, dir string) string {
	if dir == "" {
		return message
	}

	quoted := "'" + strings.ReplaceAll(dir, "'", "'\"'\"'") + "'"
	if runtime.GOOS == "windows" {
		quoted = "'" + strings.ReplaceAll(dir, "'", "''") + "'"
	}

	return strings.Replace(message, startHint, "start it with a-stack serve --state-dir "+quoted+" in another terminal, or ask the Operator to start it; then run the same command again", 1)
}

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
		return d, unreachable(serviceHint(serviceDown, dir))
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
		_, err = os.Lstat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}

		if err != nil {
			return "", storageError(err)
		}

		sidecar, openErr := openPrivate(path+suffix, false)
		if openErr != nil {
			return "", storageError(openErr)
		}

		info, err = sidecar.Stat()
		if err == nil {
			err = checkPrivate(info, false)
		}

		sidecar.Close()

		if err != nil {
			return "", err
		}
	}

	return path, nil
}
func localURL(address string) string { return fmt.Sprintf("http://%s", address) }
