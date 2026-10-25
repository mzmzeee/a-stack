//go:build linux || darwin

package stack

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// The private files of the state directory on Linux and macOS: owner-only
// modes, O_NOFOLLOW and an advisory flock. state_windows.go has the same
// functions for Windows.

func checkPrivate(info os.FileInfo, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return failure("invalid_input", "state and credential paths must be owned by this user with owner-only permissions and no symlinks")
	}
	return nil
}

// openPrivate opens path without following a symlink: for reading, or with
// write for reading and writing, created with mode 0600 when missing.
func openPrivate(path string, write bool) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_NONBLOCK
	if write {
		flags = syscall.O_CREAT | syscall.O_RDWR
	}
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// lockFile opens the lock file of a state directory and holds an exclusive
// flock on it until unlockFile.
func lockFile(path string) (*os.File, error) {
	f, err := openPrivate(path, true)
	if err != nil {
		return nil, storageError(err)
	}
	info, err := f.Stat()
	if err == nil {
		err = checkPrivate(info, false)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrStateOwned
		}
		return nil, storageError(err)
	}
	return f, nil
}

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }

// MkdirPrivate creates dir and its missing parents; dir gets mode 0700.
func MkdirPrivate(dir string) error { return os.MkdirAll(dir, 0700) }

// defaultStateRoot is the folder for the state directory when
// XDG_STATE_HOME is not set.
func defaultStateRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", failure("invalid_input", "cannot find home directory")
	}
	return filepath.Join(home, ".local", "state"), nil
}

// renameBusy is for Windows; see Rename.
func renameBusy(error) bool { return false }

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

// diskFree is the free space that this user can use on the file system of
// path.
func diskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

func checkPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return checkPrivate(info, true)
}

func openPrivateAppend(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
