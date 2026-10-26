package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"path"
	"strings"
	"testing"
)

// oversizedArchive claims a binary larger than the limit without allocating it.
func oversizedArchive(t testing.TB, windows bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if windows {
		zw := zip.NewWriter(&buf)
		if _, err := zw.CreateRaw(&zip.FileHeader{Name: "a-stack.exe", UncompressedSize64: maxArchive + 1}); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: "a-stack", Size: maxArchive + 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		// The deliberately truncated entry cannot be closed as a complete tar.
		_ = tw.Close()
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestExtractBinaryOversizedEntry(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		if binary, err := extractBinary(oversizedArchive(t, goos == "windows"), goos); err == nil || binary != nil {
			t.Fatalf("%s accepted oversized entry", goos)
		}
	}
}

func FuzzExtractTarGz(f *testing.F) {
	for _, name := range []string{"a-stack", "./a-stack", "README.md", "../a-stack", "/a-stack", "dir/../../a-stack", "a:b"} {
		f.Add(tarGz(f, map[string]string{name: "binary"}), name)
	}
	f.Add([]byte("not gzip"), "a-stack")
	f.Add(oversizedArchive(f, false), "a-stack")
	f.Fuzz(func(t *testing.T, archive []byte, name string) {
		binary, err := extractBinary(archive, "linux")
		if err == nil && (binary == nil || len(binary) > maxArchive) {
			t.Fatal("invalid binary returned")
		}
		// Keep a valid container as names mutate, so traversal checks are reached
		// even when mutations to the raw gzip fail its checksum first.
		if name == "" || len(name) > 256 || strings.ContainsRune(name, 0) || strings.HasSuffix(name, "/") {
			return
		}
		body := "fuzz binary"
		got, err := extractBinary(tarGz(t, map[string]string{name: body}), "linux")
		clean := path.Clean(name)
		safe := !path.IsAbs(name) && clean != ".." && !strings.HasPrefix(clean, "../")
		if want := safe && clean == "a-stack"; (err == nil) != want || want && string(got) != body {
			t.Fatalf("entry %q: %q, %v", name, got, err)
		}
	})
}

func FuzzExtractZip(f *testing.F) {
	for _, name := range []string{"a-stack.exe", "./a-stack.exe", "README.md", "../a-stack.exe", "/a-stack.exe", `..\a-stack.exe`, "C:/a-stack.exe"} {
		f.Add(zipArchive(f, name, "binary"), name)
	}
	f.Add(zipArchive(f, "a-stack.exe", "one", "./a-stack.exe", "two"), "a-stack.exe")
	f.Add([]byte("not zip"), "a-stack.exe")
	f.Add(oversizedArchive(f, true), "a-stack.exe")
	f.Fuzz(func(t *testing.T, archive []byte, name string) {
		binary, err := extractBinary(archive, "windows")
		if err == nil && (binary == nil || len(binary) > maxArchive) {
			t.Fatal("invalid binary returned")
		}
		if len(name) > 256 || strings.HasSuffix(name, "/") {
			return
		}
		body := "fuzz binary"
		got, err := extractBinary(zipArchive(t, name, body), "windows")
		clean := path.Clean(name)
		safe := !path.IsAbs(name) && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.ContainsAny(name, `\:`)
		if want := safe && clean == "a-stack.exe"; (err == nil) != want || want && string(got) != body {
			t.Fatalf("entry %q: %q, %v", name, got, err)
		}
	})
}
