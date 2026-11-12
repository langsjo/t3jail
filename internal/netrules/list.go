package netrules

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"strings"
)

// List is a parsed network.list. Comments and blank lines are kept so the CLI
// can edit the file without destroying what the user wrote.
type List struct {
	lines []line
}

type line struct {
	raw   string
	entry *Entry // nil for comments and blank lines
}

// DefaultList is written when an instance is created.
const DefaultList = `# Network allowlist for the t3jail VM. One entry per line; see
# ` + "`t3jail net --help`" + ` for the syntax. Edit with ` + "`t3jail net allow/remove`" + `
# or by hand (then run ` + "`t3jail net reload`" + `).

# Needed to rebuild NixOS inside the VM.
cache.nixos.org
channels.nixos.org
*.github.com
*.githubusercontent.com
`

// ParseList parses a whole file; errors name the offending line.
func ParseList(data []byte) (*List, error) {
	l := &List{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		raw := sc.Text()
		content, _, _ := strings.Cut(raw, "#")
		content = strings.TrimSpace(content)
		if content == "" {
			l.lines = append(l.lines, line{raw: raw})
			continue
		}
		e, err := Parse(content)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		l.lines = append(l.lines, line{raw: raw, entry: &e})
	}
	return l, sc.Err()
}

// Entries returns all entries in file order.
func (l *List) Entries() []Entry {
	var out []Entry
	for _, ln := range l.lines {
		if ln.entry != nil {
			out = append(out, *ln.entry)
		}
	}
	return out
}

// Add appends e unless an identical entry exists; it reports whether it did.
func (l *List) Add(e Entry) bool {
	if l.index(e) >= 0 {
		return false
	}
	l.lines = append(l.lines, line{raw: e.String(), entry: &e})
	return true
}

// Remove deletes the identical entry; it reports whether one existed.
func (l *List) Remove(e Entry) bool {
	i := l.index(e)
	if i < 0 {
		return false
	}
	l.lines = append(l.lines[:i], l.lines[i+1:]...)
	return true
}

func (l *List) index(e Entry) int {
	want := e.String()
	for i, ln := range l.lines {
		if ln.entry != nil && ln.entry.String() == want {
			return i
		}
	}
	return -1
}

func (l *List) Bytes() []byte {
	var b bytes.Buffer
	for _, ln := range l.lines {
		b.WriteString(ln.raw)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// PortsForName returns the union of ports of all entries matching name, and
// whether any entry matched.
func (l *List) PortsForName(name string) ([]PortRange, bool) {
	var ports []PortRange
	matched := false
	for _, e := range l.Entries() {
		if e.Kind != KindPrefix && e.MatchesName(name) {
			matched = true
			ports = append(ports, e.EffectivePorts()...)
		}
	}
	if !matched {
		return nil, false
	}
	return mergeRanges(ports), true
}

// PrefixRule is an explicit address allow with its ports.
type PrefixRule struct {
	Prefix netip.Prefix
	Ports  []PortRange
}

// Prefixes returns the explicit address entries.
func (l *List) Prefixes() []PrefixRule {
	var out []PrefixRule
	for _, e := range l.Entries() {
		if e.Kind == KindPrefix {
			out = append(out, PrefixRule{Prefix: e.Prefix, Ports: e.EffectivePorts()})
		}
	}
	return out
}

// Destination is one allowed address range and port range, the unit the
// firewall sets are built from.
type Destination struct {
	Prefix netip.Prefix
	Ports  PortRange
}

// Destinations expands the rule into one destination per port range.
func (r PrefixRule) Destinations() []Destination {
	dests := make([]Destination, 0, len(r.Ports))
	for _, ports := range r.Ports {
		dests = append(dests, Destination{Prefix: r.Prefix, Ports: ports})
	}
	return dests
}
