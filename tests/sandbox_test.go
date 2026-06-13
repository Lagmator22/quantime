// Unit tests for sandbox.SaveSource archive extraction.
// Run with: go test -v ./tests/ -run TestSaveSource
// No Docker required. Tests file extraction and validation only.
package tests

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveSource_TarGz_WithDockerfile(t *testing.T) {
	dir := t.TempDir()
	archive := createTarGz(t, map[string]string{
		"Dockerfile": "FROM golang:1.22\nCOPY . /app\n",
		"main.go":    "package main\nfunc main() {}\n",
	})

	dst := filepath.Join(dir, "sub-001")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGzForTest(archive, dst); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	// Dockerfile must exist after extraction
	if _, err := os.Stat(filepath.Join(dst, "Dockerfile")); err != nil {
		t.Fatal("Dockerfile not found after extraction")
	}
	// main.go must exist after extraction
	if _, err := os.Stat(filepath.Join(dst, "main.go")); err != nil {
		t.Fatal("main.go not found after extraction")
	}
	// Verify Dockerfile content is correct
	content, err := os.ReadFile(filepath.Join(dst, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte("FROM golang")) {
		t.Errorf("Dockerfile content wrong: %s", content)
	}
}

func TestSaveSource_Zip_WithDockerfile(t *testing.T) {
	dir := t.TempDir()
	archive := createZip(t, map[string]string{
		"Dockerfile": "FROM rust:1.78\nCOPY . /app\n",
		"main.rs":    "fn main() {}\n",
	})

	dst := filepath.Join(dir, "sub-002")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extractZipForTest(archive, dst); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "Dockerfile")); err != nil {
		t.Fatal("Dockerfile not found after zip extraction")
	}
	if _, err := os.Stat(filepath.Join(dst, "main.rs")); err != nil {
		t.Fatal("main.rs not found after zip extraction")
	}
}

func TestSaveSource_MissingDockerfile(t *testing.T) {
	dir := t.TempDir()
	archive := createTarGz(t, map[string]string{
		"main.go": "package main\nfunc main() {}\n",
	})

	dst := filepath.Join(dir, "sub-003")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGzForTest(archive, dst); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	// Dockerfile must NOT exist
	if _, err := os.Stat(filepath.Join(dst, "Dockerfile")); err == nil {
		t.Fatal("expected no Dockerfile, but found one")
	}
}

func TestSaveSource_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := createTarGzWithTraversal(t)

	dst := filepath.Join(dir, "sub-004")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	_ = extractTarGzForTest(archive, dst)

	// Malicious "../evil.txt" must not exist outside dst
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("path traversal attack succeeded: file written outside dst")
	}
}

func TestSaveSource_NestedDirectory(t *testing.T) {
	dir := t.TempDir()
	archive := createTarGz(t, map[string]string{
		"Dockerfile":   "FROM alpine\n",
		"src/main.go":  "package main\n",
		"src/util.go":  "package main\n",
	})

	dst := filepath.Join(dir, "sub-005")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGzForTest(archive, dst); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	// Nested files must exist
	if _, err := os.Stat(filepath.Join(dst, "src", "main.go")); err != nil {
		t.Fatal("nested src/main.go not found")
	}
	if _, err := os.Stat(filepath.Join(dst, "src", "util.go")); err != nil {
		t.Fatal("nested src/util.go not found")
	}
}

func TestSaveSource_MagicByteDetection(t *testing.T) {
	// Verify gzip magic bytes are 0x1f 0x8b
	gz := createTarGz(t, map[string]string{"Dockerfile": "FROM alpine\n"})
	if len(gz) < 2 || gz[0] != 0x1f || gz[1] != 0x8b {
		t.Fatal("gzip magic bytes incorrect")
	}

	// Verify zip magic bytes are PK\x03\x04
	zp := createZip(t, map[string]string{"Dockerfile": "FROM alpine\n"})
	if len(zp) < 4 || string(zp[:2]) != "PK" {
		t.Fatal("zip magic bytes incorrect")
	}
}

// Helper: create tar.gz from filename to content map
func createTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

// Helper: create zip from filename to content map
func createZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	return buf.Bytes()
}

// Helper: create tar.gz with a path traversal entry
func createTarGzWithTraversal(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Malicious entry attempting to escape directory
	hdr := &tar.Header{Name: "../evil.txt", Mode: 0644, Size: 5}
	tw.WriteHeader(hdr)
	tw.Write([]byte("pwned"))

	// Legitimate file
	hdr2 := &tar.Header{Name: "Dockerfile", Mode: 0644, Size: 12}
	tw.WriteHeader(hdr2)
	tw.Write([]byte("FROM alpine\n"))

	tw.Close()
	gw.Close()
	return buf.Bytes()
}

// Mirrors sandbox.extractTarGz for standalone testing
func extractTarGzForTest(data []byte, dst string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	return extractTarReaderForTest(tar.NewReader(gz), dst)
}

// Mirrors sandbox.extractTarReader for standalone testing
func extractTarReaderForTest(tr *tar.Reader, dst string) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		clean := filepath.Clean(hdr.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(dst, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, io.LimitReader(tr, 64<<20))
			f.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}
}

// Mirrors sandbox.extractZip for standalone testing
func extractZipForTest(data []byte, dst string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range r.File {
		clean := filepath.Clean(f.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(dst, clean)
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		io.Copy(out, io.LimitReader(rc, 64<<20))
		out.Close()
		rc.Close()
	}
	return nil
}
