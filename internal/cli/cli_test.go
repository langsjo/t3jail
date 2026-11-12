package cli

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/langsjo/t3jail/internal/config"
)

func TestResolveResourcesDefaultsMaximaToHost(t *testing.T) {
	cfg := config.Defaults() // 4 CPUs, 8G
	r := resolveResources(cfg, 16, 31*config.GiB+100*config.MiB)
	if r.MaxCPUs != 16 {
		t.Errorf("MaxCPUs = %d, want host count", r.MaxCPUs)
	}
	// Host RAM is rounded down to the virtio-mem block size.
	if r.MaxMemory != 31*config.GiB {
		t.Errorf("MaxMemory = %v, want 31G", r.MaxMemory)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveResourcesNeverBelowRequest(t *testing.T) {
	cfg := config.Defaults()
	cfg.CPUs, cfg.Memory = 8, 16*config.GiB
	r := resolveResources(cfg, 4, 8*config.GiB)
	if r.MaxCPUs != 8 || r.MaxMemory != 16*config.GiB {
		t.Errorf("maxima %d/%v must not be below the request", r.MaxCPUs, r.MaxMemory)
	}
	cfg.MaxCPUs, cfg.MaxMemory = 12, 20*config.GiB
	r = resolveResources(cfg, 4, 8*config.GiB)
	if r.MaxCPUs != 12 || r.MaxMemory != 20*config.GiB {
		t.Errorf("explicit maxima overridden: %d/%v", r.MaxCPUs, r.MaxMemory)
	}
}

// chunkedReader returns one chunk per Read call, like a terminal.
type chunkedReader struct{ chunks []string }

func (r *chunkedReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

func TestCopyUntilDetach(t *testing.T) {
	var out bytes.Buffer
	copyUntilDetach(&out, &chunkedReader{chunks: []string{"ls\r", "ab\x1dnot sent", "never read"}})
	if out.String() != "ls\rab" {
		t.Fatalf("forwarded %q", out.String())
	}
}

func TestPrintFileFollowsAcrossRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serial.log")
	os.WriteFile(path, []byte("first boot\n"), 0o600)
	r, w := io.Pipe()
	go printFile(path, w, true)
	lines := bufio.NewScanner(r)
	next := func() string {
		lines.Scan()
		return lines.Text()
	}
	if got := next(); got != "first boot" {
		t.Fatalf("got %q", got)
	}
	// What the daemon does on the next start.
	os.Rename(path, path+".1")
	os.WriteFile(path, []byte("second boot\n"), 0o600)
	if got := next(); got != "second boot" {
		t.Fatalf("got %q", got)
	}
}
