package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ByteSize is a size in bytes, written with binary units: "512M", "8G", "8GiB".
type ByteSize uint64

const (
	KiB ByteSize = 1 << (10 * (iota + 1))
	MiB
	GiB
	TiB
)

var units = []struct {
	suffix string
	size   ByteSize
}{{"T", TiB}, {"G", GiB}, {"M", MiB}, {"K", KiB}}

// ParseByteSize parses an integer with an optional K/M/G/T suffix (also
// accepted as KiB/KB etc., always binary). A bare number is bytes.
func ParseByteSize(s string) (ByteSize, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	t = strings.TrimSuffix(strings.TrimSuffix(t, "B"), "I")
	mult := ByteSize(1)
	for _, u := range units {
		if strings.HasSuffix(t, u.suffix) {
			mult, t = u.size, strings.TrimSuffix(t, u.suffix)
			break
		}
	}
	n, err := strconv.ParseUint(strings.TrimSpace(t), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: want a whole number with an optional K/M/G/T suffix, e.g. 8G", s)
	}
	if n > uint64(^ByteSize(0)/mult) {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return ByteSize(n) * mult, nil
}

// String formats with the largest unit that divides the size exactly.
func (b ByteSize) String() string {
	for _, u := range units {
		if b != 0 && b%u.size == 0 {
			return strconv.FormatUint(uint64(b/u.size), 10) + u.suffix
		}
	}
	return strconv.FormatUint(uint64(b), 10)
}

func (b *ByteSize) UnmarshalText(text []byte) error {
	v, err := ParseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = v
	return nil
}

func (b ByteSize) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// Set and Type make ByteSize usable as a command-line flag (pflag.Value).
func (b *ByteSize) Set(s string) error { return b.UnmarshalText([]byte(s)) }
func (b *ByteSize) Type() string       { return "size" }
