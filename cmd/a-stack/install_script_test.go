// install.sh is for Linux and macOS; install.ps1 is for Windows.

//go:build linux || darwin

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestInstallScriptChecksTheSignature runs install.sh against a file://
// mirror. The script under test is a copy that trusts testKey.
func TestInstallScriptChecksTheSignature(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}

	for _, tool := range []string{"sh", "curl", "tar", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}

	// macOS has LibreSSL, so only the cases without OpenSSL 3 run there,
	// unless Homebrew's OpenSSL 3 is on PATH.
	out, _ := exec.Command("openssl", "version").Output()
	openSSL3 := strings.HasPrefix(string(out), "OpenSSL 3")

	original, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}

	der, _ := x509.MarshalPKIXPublicKey(testKey.Public())
	realKey, _ := x509.MarshalPKIXPublicKey(releaseKey)

	script := filepath.Join(t.TempDir(), "install.sh")
	replaced := strings.Replace(string(original), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: realKey})), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), 1)
	if replaced == string(original) {
		t.Fatal("install.sh does not have the release key in PEM form")
	}

	if err = os.WriteFile(script, []byte(replaced), 0o600); err != nil {
		t.Fatal(err)
	}

	// A fake openssl that is not OpenSSL 3.
	oldSSL := t.TempDir()
	if err = os.WriteFile(filepath.Join(oldSSL, "openssl"), []byte("#!/bin/sh\necho 'LibreSSL 3.8.2'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// A fake macOS with LibreSSL, as there: on arm64, on Intel, and on Apple
	// silicon under Rosetta, where uname says x86_64.
	fakeMac := func(machine, translated string) string {
		dir := t.TempDir()
		for name, script := range map[string]string{
			"openssl": "echo 'LibreSSL 3.3.6'",
			"uname":   `case "$1" in -s) echo Darwin ;; -m) echo ` + machine + ` ;; esac`,
			"sysctl":  `[ "$*" = '-n sysctl.proc_translated' ] && echo ` + translated,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}

		return dir
	}
	mac, intel, rosetta := fakeMac("arm64", "0"), fakeMac("x86_64", "0"), fakeMac("x86_64", "1")
	// Homebrew's OpenSSL 3, which the script finds through HOMEBREW_PREFIX.
	brew := t.TempDir()
	if real, err := exec.LookPath("openssl"); err == nil && openSSL3 {
		bin := filepath.Join(brew, "opt", "openssl@3", "bin")
		if err = os.MkdirAll(bin, 0o700); err != nil || os.WriteFile(filepath.Join(bin, "openssl"), []byte("#!/bin/sh\nexec '"+real+"' \"$@\"\n"), 0o700) != nil {
			t.Fatal(err)
		}
	}

	// A Mac with OpenSSL 3 from Homebrew in its default place checks the
	// signature even when openssl on PATH is LibreSSL.
	homebrewSSL := false
	for _, file := range []string{"/opt/homebrew/opt/openssl@3/bin/openssl", "/usr/local/opt/openssl@3/bin/openssl"} {
		if _, err := os.Stat(file); err == nil {
			homebrewSSL = true
		}
	}

	const require = "A_STACK_REQUIRE_SIGNATURE=1"
	const handCheck = "To check it, install OpenSSL 3 (on macOS: brew install openssl@3) and run the script again, or see https://github.com/mzmzeee/a-stack/blob/main/docs/install.md#how-releases-are-verified"

	for _, c := range []struct {
		name, tag, sig, path string
		env                  []string
		ok                   bool
		want, stderr         string
		archive              string // GOOS_GOARCH; default: this machine's
	}{
		{name: "good", tag: "v0.2.0", sig: "good", ok: true, want: "Installed a-stack v0.2.0"},
		{name: "bad", tag: "v0.2.0", sig: "bad", want: "the signature of SHA256SUMS is not valid"},
		{name: "missing on a signed release", tag: "v0.1.5", sig: "missing", want: "every release since v0.1.5 is signed"},
		{name: "missing on latest", tag: "latest", sig: "missing", want: "every release since v0.1.5 is signed"},
		{name: "missing on an old release", tag: "v0.1.4", sig: "missing", ok: true, want: "Installed a-stack v0.1.4", stderr: "a-stack v0.1.4 has no release signature"},
		{name: "bad on an old release", tag: "v0.1.4", sig: "bad", want: "the signature of SHA256SUMS is not valid"},
		{name: "required and good", tag: "v0.2.0", sig: "good", env: []string{require}, ok: true, want: "Installed a-stack v0.2.0"},
		{name: "required on an old release", tag: "v0.1.4", sig: "missing", env: []string{require}, want: "a-stack v0.1.4 has no release signature, as every release before v0.1.5, and A_STACK_REQUIRE_SIGNATURE=1 needs one; nothing was installed"},
		{name: "no OpenSSL 3", tag: "v0.2.0", sig: "bad", path: oldSSL, ok: true, want: "Installed a-stack v0.2.0", stderr: handCheck},
		{name: "required without OpenSSL 3", tag: "v0.2.0", sig: "good", path: oldSSL, env: []string{require}, want: "OpenSSL 3 was not found, so the release signature cannot be checked, which A_STACK_REQUIRE_SIGNATURE=1 needs; nothing was installed"},
		{name: "macOS", tag: "v0.2.0", sig: "good", path: mac, ok: true, want: "Installed a-stack v0.2.0", archive: "darwin_arm64"},
		{name: "macOS with Homebrew OpenSSL 3, good", tag: "v0.2.0", sig: "good", path: mac, env: []string{"HOMEBREW_PREFIX=" + brew, require}, ok: true, want: "Installed a-stack v0.2.0", archive: "darwin_arm64"},
		{name: "macOS with Homebrew OpenSSL 3, bad", tag: "v0.2.0", sig: "bad", path: mac, env: []string{"HOMEBREW_PREFIX=" + brew}, want: "the signature of SHA256SUMS is not valid", archive: "darwin_arm64"},
		{name: "macOS on Intel", tag: "v0.2.0", sig: "good", path: intel, ok: true, want: "Installed a-stack v0.2.0", archive: "darwin_amd64"},
		{name: "macOS under Rosetta", tag: "v0.2.0", sig: "good", path: rosetta, ok: true, want: "Installed a-stack v0.2.0", archive: "darwin_arm64"},
	} {
		t.Run(c.name, func(t *testing.T) {
			homebrew := slices.ContainsFunc(c.env, func(e string) bool { return strings.HasPrefix(e, "HOMEBREW_PREFIX=") })
			if (c.path == "" || homebrew) && !openSSL3 {
				t.Skip("OpenSSL 3 is not installed")
			}

			if c.path == oldSSL && homebrewSSL {
				t.Skip("this machine has OpenSSL 3 from Homebrew")
			}

			mirror := t.TempDir()

			tag := c.tag
			if tag == "latest" {
				tag = "v0.2.0"
			}

			archive := tarGz(t, map[string]string{"a-stack": fakeBinary(tag)})
			name := releaseArchive(runtime.GOOS, runtime.GOARCH)
			if c.archive != "" {
				goos, goarch, _ := strings.Cut(c.archive, "_")
				name = releaseArchive(goos, goarch)
			}

			sum := sha256.Sum256(archive)
			sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")

			sig := ed25519.Sign(testKey, sums)
			if c.sig == "bad" {
				sig[0]++
			}

			for _, dir := range []string{filepath.Join(mirror, "download", tag), filepath.Join(mirror, "latest", "download")} {
				files := map[string][]byte{name: archive, "SHA256SUMS": sums}
				if c.sig != "missing" {
					files["SHA256SUMS.sig"] = sig
				}

				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}

				for name, data := range files {
					if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}

			bin := t.TempDir()
			cmd := exec.Command("sh", script)
			cmd.Env = append(os.Environ(), "HOMEBREW_PREFIX=", "A_STACK_REQUIRE_SIGNATURE=", "A_STACK_BASE_URL=file://"+mirror, "A_STACK_VERSION="+c.tag, "A_STACK_INSTALL_DIR="+bin, "PATH="+c.path+":"+os.Getenv("PATH"))
			cmd.Env = append(cmd.Env, c.env...)
			var stdout, stderr strings.Builder

			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if (err == nil) != c.ok || !strings.Contains(stdout.String()+stderr.String(), c.want) || !strings.Contains(stderr.String(), c.stderr) {
				t.Fatalf("exit %v, stdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}

			if _, err := os.Stat(filepath.Join(bin, "a-stack")); (err == nil) != c.ok {
				t.Fatalf("installed=%v, want %v", err == nil, c.ok)
			}
		})
	}
}
