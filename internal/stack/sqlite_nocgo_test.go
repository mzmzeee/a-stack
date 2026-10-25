//go:build !cgo

package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A plain go install on a Mac without a C compiler builds without cgo. serve
// must say so, instead of the general storage message, and leave the state
// directory alone. Run it with CGO_ENABLED=0 go test -run TestStartWithoutCgo.
func TestStartWithoutCgo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	_, err := Start(ctx, dir, "")
	requireCode(t, err, "storage_unavailable")
	if !strings.Contains(err.Error(), "built without cgo") {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the state directory was made:", err)
	}
}
