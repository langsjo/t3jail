package cli

import (
	"slices"
	"testing"
)

func TestPickerKeys(t *testing.T) {
	p := &picker{choices: make([]choice, 3)}
	for _, k := range []string{" ", "j", "\r", " ", "\x1b[B", "\r", "\r", " ", "k", "\x1b[A", "\x1b[A"} {
		if done, _ := p.key(k); done {
			t.Fatalf("key %q ended the picker", k)
		}
	}
	if want := []choice{allowNow, allowAlways, keepBlocked}; !slices.Equal(p.choices, want) || p.cursor != 0 {
		t.Errorf("choices %v cursor %d, want %v cursor 0", p.choices, p.cursor, want)
	}
	for range 5 {
		p.key("j")
	}
	if p.cursor != 3 {
		t.Fatalf("cursor %d after moving past the end, want 3 (submit)", p.cursor)
	}
	if done, apply := p.key("\r"); !done || !apply {
		t.Error("enter on submit does not apply")
	}
	if !slices.Equal(p.choices, []choice{allowNow, allowAlways, keepBlocked}) {
		t.Errorf("submitting changed the choices to %v", p.choices)
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
