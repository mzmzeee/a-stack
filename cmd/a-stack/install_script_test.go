package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInstallScriptChecksTheSignature runs install.sh against a file://
// mirror. The script under test is a copy that trusts testKey.
func TestInstallScriptChecksTheSignature(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}
	for _, tool := range []string{"sh", "curl", "openssl", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	if out, _ := exec.Command("openssl", "version").Output(); !strings.HasPrefix(string(out), "OpenSSL 3") {
		t.Skip("OpenSSL 3 is not installed")
	}
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

	for _, c := range []struct {
		name, tag, sig, path string
		ok                   bool
		want                 string
	}{
		{"good", "v0.2.0", "good", "", true, "Installed a-stack v0.2.0"},
		{"bad", "v0.2.0", "bad", "", false, "the signature of SHA256SUMS is not valid"},
		{"missing on a signed release", "v0.1.5", "missing", "", false, "every release since v0.1.5 is signed"},
		{"missing on latest", "latest", "missing", "", false, "every release since v0.1.5 is signed"},
		{"missing on an old release", "v0.1.4", "missing", "", true, "a-stack v0.1.4 has no release signature"},
		{"bad on an old release", "v0.1.4", "bad", "", false, "the signature of SHA256SUMS is not valid"},
		{"no OpenSSL 3", "v0.2.0", "bad", oldSSL, true, "OpenSSL 3 was not found, so the release signature was not checked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mirror := t.TempDir()
			tag := c.tag
			if tag == "latest" {
				tag = "v0.2.0"
			}
			archive := tarGz(t, map[string]string{"a-stack": fakeBinary(tag)})
			sums := (&fakeRelease{}).sums(archive)
			sig := ed25519.Sign(testKey, sums)
			if c.sig == "bad" {
				sig[0]++
			}
			for _, dir := range []string{filepath.Join(mirror, "download", tag), filepath.Join(mirror, "latest", "download")} {
				files := map[string][]byte{"a-stack_linux_" + runtime.GOARCH + ".tar.gz": archive, "SHA256SUMS": sums}
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
			cmd.Env = append(os.Environ(), "A_STACK_BASE_URL=file://"+mirror, "A_STACK_VERSION="+c.tag, "A_STACK_INSTALL_DIR="+bin, "PATH="+c.path+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if (err == nil) != c.ok || !strings.Contains(string(out), c.want) {
				t.Fatalf("exit %v, output:\n%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(bin, "a-stack")); (err == nil) != c.ok {
				t.Fatalf("installed=%v, want %v", err == nil, c.ok)
			}
		})
	}
}
