//go:build linux || darwin

package stack

import "path/filepath"

// ResolvePath resolves an existing path, including directory links.
func ResolvePath(path string) (string, error) { return filepath.EvalSymlinks(path) }

func localStatePath(string) bool { return true }
