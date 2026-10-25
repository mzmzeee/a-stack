package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

const (
	defaultReleases = "https://github.com/mzmzeee/a-stack/releases"
	installScript   = defaultReleases + "/latest/download/install.sh"
	maxArchive      = 256 << 20 // the archive and the binary in it
	maxSums         = 1 << 20
)

// releaseKey checks SHA256SUMS.sig, the raw Ed25519 signature that the
// release workflow makes over SHA256SUMS. install.sh has the same key as PEM.
// Tests replace it.
var releaseKey = ed25519.PublicKey(mustBase64("NkJiNRRt3iLovFX+EVrAxA1Z7a3diAw6F3PhyC9+D08="))

// signedSince is the first signed release. Older releases have no
// SHA256SUMS.sig; update installs them with a warning.
var signedSince = [3]int{0, 1, 5}

func mustBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// releaseTag is a release version as the release workflow builds it.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// Tests replace these.
var (
	executable    = os.Executable
	serviceActive = func() bool {
		if runtime.GOOS == "darwin" {
			return launchdState(stack.LaunchdLabel) == "running"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", "a-stack").Run() == nil
	}
)

// updateSystems are the systems with release archives.
var updateSystems = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}

// releaseArchive names the release archive of a system. The extension
// belongs to the archive format that extractBinary reads.
func releaseArchive(goos, goarch string) string {
	return "a-stack_" + goos + "_" + goarch + ".tar.gz"
}

func updateFailed(message string) error {
	return &stack.Error{Code: "integration_unavailable", Message: message, Retryable: true}
}

// releases downloads from GitHub Releases over HTTPS, also after redirects.
// A_STACK_BASE_URL names a mirror, as for install.sh; a mirror may also use
// http and file URLs.
type releases struct {
	base   string
	mirror bool
	client *http.Client
}

func newReleases() releases {
	base, mirror := strings.TrimRight(os.Getenv("A_STACK_BASE_URL"), "/"), true
	if base == "" {
		base, mirror = defaultReleases, false
	}
	allowed := func(u *url.URL) bool { return u.Scheme == "https" || mirror && u.Scheme == "http" }
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !allowed(req.URL) {
			return errors.New("redirect to a URL that is not HTTPS")
		}
		return nil
	}}
	return releases{base: base, mirror: mirror, client: client}
}

// get downloads a release file. With optional, a file that does not exist
// returns nil and no error.
func (r releases) get(rawURL string, limit int64, optional bool) ([]byte, error) {
	var body io.ReadCloser
	if file, ok := strings.CutPrefix(rawURL, "file://"); ok && r.mirror {
		f, err := os.Open(file)
		if optional && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, updateFailed("cannot read " + rawURL)
		}
		body = f
	} else {
		if u, err := url.Parse(rawURL); err != nil || u.Scheme != "https" && !(r.mirror && u.Scheme == "http") {
			return nil, invalid("release URLs must use HTTPS")
		}
		resp, err := r.client.Get(rawURL)
		if err != nil {
			return nil, updateFailed("download failed: " + rawURL)
		}
		if optional && resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, updateFailed("download failed: " + rawURL + ": " + resp.Status)
		}
		body = resp.Body
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, updateFailed("download failed: " + rawURL)
	}
	if int64(len(data)) > limit {
		return nil, updateFailed("download is too large: " + rawURL)
	}
	return data, nil
}

// latest reads the version of the latest release from the redirect of
// /latest to /tag/vX.Y.Z, so it needs no GitHub API and skips prereleases.
func (r releases) latest() (string, error) {
	fail := updateFailed("cannot find the latest release at " + r.base + "; name one with --version")
	if !strings.HasPrefix(r.base, "https://") && !(r.mirror && strings.HasPrefix(r.base, "http://")) {
		return "", fail
	}
	client := *r.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Head(r.base + "/latest")
	if err != nil {
		return "", fail
	}
	resp.Body.Close()
	location, err := resp.Location()
	if err != nil {
		return "", fail
	}
	_, tag, ok := strings.Cut(location.Path, "/tag/")
	if !ok || !releaseTag.MatchString(tag) {
		return "", fail
	}
	return tag, nil
}

// download returns the verified a-stack binary of a release. It trusts
// SHA256SUMS only after its signature checks out with releaseKey; signed is
// false for a release from before signedSince that has no signature.
func (r releases) download(tag string) (binary []byte, signed bool, err error) {
	dir := r.base + "/download/" + tag + "/"
	sums, err := r.get(dir+"SHA256SUMS", maxSums, false)
	if err != nil {
		return nil, false, err
	}
	sig, err := r.get(dir+"SHA256SUMS.sig", 1024, true)
	if err != nil {
		return nil, false, err
	}
	n, _, _ := semver(tag)
	if sig == nil && slices.Compare(n[:], signedSince[:]) >= 0 {
		return nil, false, &stack.Error{Code: "integration_unavailable", Message: "release " + tag + " has no SHA256SUMS.sig, but every release since v0.1.5 is signed; nothing changed"}
	}
	if sig != nil && !ed25519.Verify(releaseKey, sums, sig) {
		return nil, false, &stack.Error{Code: "integration_unavailable", Message: "the signature of SHA256SUMS for " + tag + " is not valid; nothing changed"}
	}
	name := releaseArchive(runtime.GOOS, runtime.GOARCH)
	archive, err := r.get(dir+name, maxArchive, false)
	if err != nil {
		return nil, false, err
	}
	sum := sha256.Sum256(archive)
	if !slices.Contains(strings.Split(string(sums), "\n"), hex.EncodeToString(sum[:])+"  "+name) {
		return nil, false, &stack.Error{Code: "integration_unavailable", Message: "checksum mismatch for " + name + "; nothing changed"}
	}
	binary, err = extractBinary(archive)
	return binary, sig != nil, err
}

// extractBinary returns the a-stack file of a release archive. It refuses an
// archive with an absolute path or a path that leaves the archive.
func extractBinary(archive []byte) ([]byte, error) {
	bad := &stack.Error{Code: "integration_unavailable", Message: "the release archive is not valid; nothing changed"}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, bad
	}
	tr := tar.NewReader(io.LimitReader(gz, 4*maxArchive))
	var binary []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, bad
		}
		name := path.Clean(h.Name)
		if path.IsAbs(h.Name) || name == ".." || strings.HasPrefix(name, "../") {
			return nil, &stack.Error{Code: "integration_unavailable", Message: "the release archive has an unsafe path; nothing changed"}
		}
		if name != "a-stack" {
			continue
		}
		if binary != nil || h.Typeflag != tar.TypeReg || h.Size > maxArchive {
			return nil, bad
		}
		if binary, err = io.ReadAll(io.LimitReader(tr, maxArchive)); err != nil {
			return nil, bad
		}
	}
	if binary == nil {
		return nil, bad
	}
	return binary, nil
}

// semver splits v1.2.3-rc.1 into its numbers and prerelease, or returns false.
func semver(v string) ([3]int, string, bool) {
	var n [3]int
	if !releaseTag.MatchString(v) {
		return n, "", false
	}
	core, pre, _ := strings.Cut(v[1:], "-")
	for i, part := range strings.Split(core, ".") {
		n[i], _ = strconv.Atoi(part)
	}
	return n, pre, true
}

// newer reports whether release a is newer than b. A version that is not a
// release, such as a development build, is older than every release.
func newer(a, b string) bool {
	na, pa, okA := semver(a)
	nb, pb, okB := semver(b)
	if !okA || !okB {
		return okA
	}
	if c := slices.Compare(na[:], nb[:]); c != 0 {
		return c > 0
	}
	return pa != pb && (pa == "" || pb != "" && pa > pb)
}

// runVersion runs an a-stack binary's version command.
func runVersion(binary string) (string, error) {
	var out []byte
	var err error
	for range 5 { // a just-written binary can be busy while another process forks
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err = exec.CommandContext(ctx, binary, "version").Output()
		cancel()
		if !errors.Is(err, syscall.ETXTBSY) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var result struct {
		Data struct{ Version string } `json:"data"`
	}
	if err != nil || json.Unmarshal(out, &result) != nil {
		return "", errors.New("does not run")
	}
	return result.Data.Version, nil
}

func updateCommand(release versionFlag, check, machine bool, stdout io.Writer) error {
	target := release.value
	if !slices.Contains(updateSystems, runtime.GOOS+"/"+runtime.GOARCH) {
		return invalid("a-stack update supports Linux and macOS on amd64 and arm64 only")
	}
	if release.set && !releaseTag.MatchString(target) {
		return invalid("--version needs a release such as v0.1.3")
	}
	if check && target != "" {
		return invalid("--check takes no --version")
	}
	current := buildVersion()
	r := newReleases()
	write := func(data map[string]any, lines ...string) error {
		if machine {
			return json.NewEncoder(stdout).Encode(map[string]any{"data": data})
		}
		_, err := io.WriteString(stdout, strings.Join(lines, "\n")+"\n")
		return err
	}
	if check {
		latest, err := r.latest()
		if err != nil {
			return err
		}
		available := latest != current && !newer(current, latest)
		line := "a-stack " + current + " is the latest version"
		if available {
			line = "a-stack " + latest + " is available; you have " + current + ". Run a-stack update to install it."
		} else if latest != current {
			line = "a-stack " + current + " is newer than the latest release " + latest
		}
		return write(map[string]any{"version": current, "latest": latest, "update_available": available}, line)
	}
	if target == "" && version == "" {
		return invalid("this a-stack is a development build without a release version; run a-stack update --version vX.Y.Z to install a release")
	}

	exe, err := executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return updateFailed("cannot find the running a-stack binary")
	}
	data := map[string]any{"previous": current, "version": current, "path": exe, "updated": false}
	if target == "" {
		if target, err = r.latest(); err != nil {
			return err
		}
		if target == current || newer(current, target) {
			return write(data, "a-stack "+current+" is already the latest version")
		}
	} else if target == current {
		return write(data, "a-stack "+current+" is already installed in "+exe)
	}
	// Check the folder before the download, and write next to the binary
	// so that the rename replaces it in one step.
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".a-stack-update-")
	if err != nil {
		return &stack.Error{Code: "forbidden", Message: "cannot write to " + dir + "; install a-stack in a folder you own instead: curl -fsSL " + installScript + " | A_STACK_INSTALL_DIR=~/.local/bin sh"}
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	binary, signed, err := r.download(target)
	if err != nil {
		return err
	}
	info, err := os.Stat(exe)
	mode := fs.FileMode(0o755)
	if err == nil {
		mode = info.Mode().Perm()
	}
	if _, err = tmp.Write(binary); err == nil {
		err = tmp.Chmod(mode | 0o100)
	}
	if err == nil {
		err = tmp.Sync() // the rename must never publish an empty file
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return updateFailed("cannot write to " + dir + "; nothing changed")
	}
	if got, err := runVersion(tmp.Name()); err != nil || got != target {
		return updateFailed("the downloaded a-stack " + target + " does not run on this system; nothing changed")
	}
	if err = os.Rename(tmp.Name(), exe); err != nil {
		return updateFailed("cannot replace " + exe + "; nothing changed")
	}
	if err = syncDir(dir); err != nil {
		return updateFailed("replaced " + exe + " with a-stack " + target + ", but cannot sync " + dir + " to disk")
	}

	data["version"], data["updated"], data["signed"] = target, true, signed
	lines := []string{"Updated a-stack " + current + " -> " + target + " in " + exe}
	warnings := []string{}
	if !signed {
		warnings = append(warnings, "Warning: a-stack "+target+" has no release signature, as every release before v0.1.5; only its SHA-256 checksum was checked.")
	}
	if newer(current, target) {
		warnings = append(warnings, "Warning: a-stack "+target+" is older than "+current+" and may not open the current database: serve then stops with exit code 4. To go back, run a-stack update --version "+current+", or restore the state.db.pre-vN copy that a schema migration left in the state directory (changes made after it are lost).")
	}
	data["warnings"] = warnings
	lines = append(lines, warnings...)
	refreshed, ok := refreshSkills(exe)
	if !ok {
		lines = append(lines, "a-stack "+target+" cannot update the agent skill; the copies stay as they are.")
	} else if len(refreshed) > 0 {
		lines = append(lines, "Updated the a-stack skill in:")
		for _, folder := range refreshed {
			lines = append(lines, "  "+folder)
		}
	}
	data["skills"] = refreshed
	hint := "If a-stack serve is running, stop it with Ctrl-C and run a-stack serve again."
	if serviceActive() {
		hint = "Restart the service: " + stack.ServiceCommand("restart")
	}
	data["restart_hint"] = hint
	return write(data, append(lines, hint, "Then run a-stack to open the view.")...)
}

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// refreshSkills runs the new binary's skill install --refresh for the user
// and the project skill copies that exist, so they match the new version.
// It creates no skill folders. ok is false when the new binary cannot do it.
func refreshSkills(exe string) (refreshed []string, ok bool) {
	refreshed = []string{}
	for _, project := range []bool{false, true} {
		folders, err := skillFolders(project)
		if err != nil || !slices.ContainsFunc(folders, func(f string) bool { _, err := os.Lstat(f); return err == nil }) {
			continue
		}
		args := []string{"skill", "install", "--refresh", "--json"}
		if project {
			args = append(args, "--project")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, exe, args...).Output()
		cancel()
		var result struct {
			Data struct{ Installed []string } `json:"data"`
		}
		if err != nil || json.Unmarshal(out, &result) != nil {
			return refreshed, false
		}
		for _, folder := range result.Data.Installed {
			if !slices.Contains(refreshed, folder) {
				refreshed = append(refreshed, folder)
			}
		}
	}
	return refreshed, true
}
