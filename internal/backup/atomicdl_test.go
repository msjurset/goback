package backup

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicDownloadSuccess(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.tar")

	body := "hello world"
	n, err := AtomicDownload(dest, int64(len(body)), func(w io.Writer) (int64, error) {
		return io.Copy(w, strings.NewReader(body))
	})
	if err != nil {
		t.Fatalf("AtomicDownload: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("n = %d, want %d", n, len(body))
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("contents = %q, want %q", got, body)
	}
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should be gone, got err=%v", err)
	}
}

func TestAtomicDownloadFnError(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.tar")

	wantErr := errors.New("network drop")
	_, err := AtomicDownload(dest, -1, func(w io.Writer) (int64, error) {
		_, _ = w.Write([]byte("partial..."))
		return 10, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest should not exist after error, got err=%v", err)
	}
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should be removed after error, got err=%v", err)
	}
}

func TestAtomicDownloadSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.tar")

	body := "five!"
	_, err := AtomicDownload(dest, 999, func(w io.Writer) (int64, error) {
		return io.Copy(w, strings.NewReader(body))
	})
	if err == nil {
		t.Fatal("expected size-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "size mismatch") {
		t.Errorf("err = %v, want size mismatch", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest should not exist on size mismatch")
	}
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should be removed on size mismatch")
	}
}

func TestAtomicDownloadUnknownSize(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.tar")

	body := "any size accepted"
	_, err := AtomicDownload(dest, -1, func(w io.Writer) (int64, error) {
		return io.Copy(w, strings.NewReader(body))
	})
	if err != nil {
		t.Fatalf("AtomicDownload with unknown size: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != body {
		t.Errorf("contents = %q, want %q", got, body)
	}
}

func TestAtomicDownloadOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.tar")

	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := "new content"
	_, err := AtomicDownload(dest, int64(len(body)), func(w io.Writer) (int64, error) {
		return io.Copy(w, strings.NewReader(body))
	})
	if err != nil {
		t.Fatalf("AtomicDownload: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != body {
		t.Errorf("contents = %q, want %q", got, body)
	}
}

func TestAtomicDownloadCreateError(t *testing.T) {
	// dest is inside a path that doesn't exist; os.Create must fail
	dest := filepath.Join(t.TempDir(), "no", "such", "dir", "backup.tar")
	_, err := AtomicDownload(dest, -1, func(w io.Writer) (int64, error) {
		t.Fatal("fn must not be called when create fails")
		return 0, nil
	})
	if err == nil {
		t.Fatal("expected error when destination directory does not exist")
	}
	// no partial leak (none was created)
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should not exist, got err=%v", err)
	}
}
