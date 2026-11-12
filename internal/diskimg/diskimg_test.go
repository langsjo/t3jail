package diskimg

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCopySparse(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "base.img")
	// 8 MiB image with data at the start, in the middle and a partial tail.
	content := make([]byte, 8<<20+123)
	copy(content[0:], "boot sector")
	copy(content[5<<20:], "root fs")
	copy(content[len(content)-3:], "end")
	if err := os.WriteFile(src, content, 0o400); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "disk.raw")
	const size = 64 << 20
	if err := CopySparse(src, dst, size); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != size || !bytes.Equal(got[:len(content)], content) || bytes.ContainsFunc(got[len(content):], func(r rune) bool { return r != 0 }) {
		t.Fatal("copied disk content differs from the image")
	}
	fi, _ := os.Stat(dst)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("disk mode %v, want 0600 even though the image is read-only", fi.Mode().Perm())
	}
	// Only the three data chunks should be allocated (tmpfs/ext4 512B blocks).
	if allocated := fi.Sys().(*syscall.Stat_t).Blocks * 512; allocated > 4<<20 {
		t.Errorf("%d bytes allocated; zero chunks were written", allocated)
	}
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
}

func TestCopySparseRefusesSmallerSize(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "base.img")
	os.WriteFile(src, make([]byte, 2<<20), 0o600)
	if err := CopySparse(src, filepath.Join(dir, "disk.raw"), 1<<20); err == nil {
		t.Fatal("expected error")
	}
}

func TestGrow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.raw")
	os.WriteFile(path, []byte("data"), 0o600)
	if err := Grow(path, 1<<20); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Size() != 1<<20 {
		t.Fatalf("size %d", fi.Size())
	}
	if err := Grow(path, 10); err == nil {
		t.Fatal("shrinking accepted")
	}
}

func TestCheckGrow(t *testing.T) {
	const gib = 1 << 30
	if err := CheckGrow(64*gib, 70*gib); err != nil {
		t.Errorf("growing: %v", err)
	}
	if err := CheckGrow(64*gib, 64*gib); err != nil {
		t.Errorf("same size: %v", err)
	}
	if err := CheckGrow(64*gib, 32*gib); err == nil {
		t.Error("shrinking accepted")
	}
	// cloud-hypervisor rejects sizes that are not whole sectors.
	if err := CheckGrow(64*gib, 64*gib+100); err == nil {
		t.Error("size that is not a whole number of sectors accepted")
	}
}
