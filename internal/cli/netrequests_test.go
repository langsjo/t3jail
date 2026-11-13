package cli

import (
	"slices"
	"testing"
)

func TestPickerKeys(t *testing.T) {
	p := &picker{choices: make([]choice, 3)}
	for _, k := range []string{" ", "j", " ", " ", "\x1b[B", "\x1b[B", " ", " ", " ", "k", "\x1b[A", "\x1b[A"} {
		if done, _ := p.key(k); done {
			t.Fatalf("key %q ended the picker", k)
		}
	}
	if want := []choice{allowNow, allowAlways, keepBlocked}; !slices.Equal(p.choices, want) || p.cursor != 0 {
		t.Errorf("choices %v cursor %d, want %v cursor 0", p.choices, p.cursor, want)
	}
	if done, apply := p.key("\r"); !done || !apply {
		t.Error("enter does not apply")
	}
	if done, apply := p.key("q"); !done || apply {
		t.Error("q does not quit without applying")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("héllo world", 6); got != "héllo…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("short", 80); got != "short" {
		t.Errorf("truncate = %q", got)
	}
}
