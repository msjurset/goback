package backup

import (
	"fmt"
	"io"
	"os"
)

// AtomicDownload writes via fn(w) to dest+".partial" and atomically renames it
// to dest on success. If expectedSize is non-negative and the bytes written
// don't match, the partial is removed and an error is returned. On any error
// path the partial is removed so retention/listing can never see a truncated
// backup as the latest file.
func AtomicDownload(dest string, expectedSize int64, fn func(io.Writer) (int64, error)) (int64, error) {
	partial := dest + ".partial"

	f, err := os.Create(partial)
	if err != nil {
		return 0, fmt.Errorf("creating %s: %w", partial, err)
	}

	n, copyErr := fn(f)
	closeErr := f.Close()

	if copyErr != nil {
		_ = os.Remove(partial)
		return n, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(partial)
		return n, fmt.Errorf("closing %s: %w", partial, closeErr)
	}
	if expectedSize >= 0 && n != expectedSize {
		_ = os.Remove(partial)
		return n, fmt.Errorf("size mismatch: wrote %d bytes, expected %d", n, expectedSize)
	}
	if err := os.Rename(partial, dest); err != nil {
		_ = os.Remove(partial)
		return n, fmt.Errorf("renaming %s -> %s: %w", partial, dest, err)
	}
	return n, nil
}
