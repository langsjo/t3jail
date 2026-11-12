// Package diskimg creates the VM disk from the base image.
package diskimg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

const chunk = 1 << 20

// CopySparse copies src to a new file dst without allocating all-zero 1 MiB
// chunks, then extends it to size (which must not be smaller than src).
// dst is written to a temporary name and renamed into place when complete.
func CopySparse(src, dst string, size int64) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if size < fi.Size() {
		return fmt.Errorf("disk size %d is smaller than the base image (%d bytes)", size, fi.Size())
	}

	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Left over from an interrupted copy.
		os.Remove(tmp)
		out, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		if err != nil {
			os.Remove(tmp)
		}
	}()

	buf := make([]byte, chunk)
	zero := make([]byte, chunk)
	var off int64
	for {
		n, rerr := io.ReadFull(in, buf)
		if n > 0 && !bytes.Equal(buf[:n], zero[:n]) {
			if _, err := out.WriteAt(buf[:n], off); err != nil {
				return err
			}
		}
		off += int64(n)
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := out.Truncate(size); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Grow extends an existing disk file to size.
func Grow(path string, size int64) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := CheckGrow(fi.Size(), size); err != nil {
		return err
	}
	return os.Truncate(path, size)
}

// SectorSize is the unit disk sizes must be a multiple of.
const SectorSize = 512

// CheckGrow validates growing a disk from current to size bytes. Shrinking is
// refused because it would cut off the guest's filesystem.
func CheckGrow(current, size int64) error {
	switch {
	case size%SectorSize != 0:
		return fmt.Errorf("the disk size must be a multiple of %d bytes", SectorSize)
	case size < current:
		return fmt.Errorf("cannot shrink the disk from %d to %d bytes", current, size)
	}
	return nil
}
