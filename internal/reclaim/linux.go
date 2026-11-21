package reclaim

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
)

const (
	ReclaimPath = "/sys/fs/cgroup/memory.reclaim"
	virtioBus   = "/sys/bus/virtio/devices"
	balloonID   = 5
	reporting   = 5
)

type Linux struct{}

func (Linux) Sample() (Sample, error) {
	stallUS, err := readProc("/proc/pressure/memory", parseStall)
	if err != nil {
		return Sample{}, err
	}
	refaultPages, err := readProc("/proc/vmstat", parseRefaults)
	if err != nil {
		return Sample{}, err
	}
	fileCacheKiB, err := readProc("/proc/meminfo", parseFileCache)
	if err != nil {
		return Sample{}, err
	}
	return Sample{StallUS: stallUS, RefaultPages: refaultPages, FileCacheKiB: fileCacheKiB}, nil
}

func (Linux) Reclaim(kib uint64) error {
	return writeReclaim(ReclaimPath, kib)
}

// writeReclaim uses raw syscalls: os.File registers sysfs files with Go's
// poller, which turns the EAGAIN memory.reclaim returns on a shortfall into a
// wait for a writability event that never comes.
func writeReclaim(path string, kib uint64) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer syscall.Close(fd)
	// No retry on EINTR: the kernel may have reclaimed part of the request
	// already, and the next tick decides afresh.
	if _, err := syscall.Write(fd, []byte(fmt.Sprintf("%dK swappiness=0", kib))); err != nil {
		return &os.PathError{Op: "write", Path: path, Err: err}
	}
	return nil
}

func ReportingEnabled() (bool, error) {
	return reportingEnabled(os.DirFS(virtioBus))
}

func reportingEnabled(devices fs.FS) (bool, error) {
	entries, err := fs.ReadDir(devices, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		id, err := fs.ReadFile(devices, path.Join(entry.Name(), "device"))
		if err != nil {
			return false, err
		}
		device, err := strconv.ParseUint(strings.TrimSpace(string(id)), 0, 32)
		if err != nil {
			return false, fmt.Errorf("%s/device: %w", entry.Name(), err)
		}
		if device != balloonID {
			continue
		}
		// features keeps its last value after the driver unbinds or fails to
		// probe, and without a driver nothing reports free pages.
		_, err = fs.Stat(devices, path.Join(entry.Name(), "driver"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		data, err := fs.ReadFile(devices, path.Join(entry.Name(), "features"))
		if err != nil {
			return false, err
		}
		bits := strings.TrimSpace(string(data))
		if bits == "" || strings.Trim(bits, "01") != "" {
			return false, fmt.Errorf("%s/features: invalid virtio feature bit string", entry.Name())
		}
		if len(bits) > reporting && bits[reporting] == '1' {
			return true, nil
		}
	}
	return false, nil
}

func readProc(path string, parse func(string) (uint64, error)) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := parse(string(data))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return value, nil
}

func parseStall(data string) (uint64, error) {
	fields := lineFields(data, "some")
	for _, field := range fields {
		if value, ok := strings.CutPrefix(field, "total="); ok {
			return strconv.ParseUint(value, 10, 64)
		}
	}
	return 0, errors.New("missing some total")
}

func parseRefaults(data string) (uint64, error) {
	fields := lineFields(data, "workingset_refault_file")
	if len(fields) != 1 {
		return 0, errors.New("expected one workingset_refault_file value")
	}
	return strconv.ParseUint(fields[0], 10, 64)
}

func parseFileCache(data string) (uint64, error) {
	var total uint64
	for _, key := range []string{"Active(file):", "Inactive(file):"} {
		fields := lineFields(data, key)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, fmt.Errorf("expected %s value in kB", key)
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s %w", key, err)
		}
		if total+value < total {
			return 0, errors.New("file cache total overflows")
		}
		total += value
	}
	return total, nil
}

func lineFields(data, key string) []string {
	for line := range strings.SplitSeq(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == key {
			return fields[1:]
		}
	}
	return nil
}
