package cli

import (
	"os"
	"testing"
)

func TestDiskResizeGrowsOnlyWhenStopped(t *testing.T) {
	inst := testInstance(t)
	inst.Ensure()
	os.WriteFile(inst.Disk(), nil, 0o600)
	os.Truncate(inst.Disk(), 1<<30)

	if err := runCLI("disk", "resize", "512M"); err == nil {
		t.Error("disk shrunk")
	}
	if err := runCLI("disk", "resize", "2G"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(inst.Disk()); fi.Size() != 2<<30 {
		t.Errorf("size %d", fi.Size())
	}

	// A running daemon holds its lock.
	unlock, err := tryLockRuntime(inst, "daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := runCLI("disk", "resize", "3G"); err == nil {
		t.Error("resized while the daemon lock was held")
	}
}
