package daemon

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/langsjo/t3jail/internal/control"
)

func TestParseRequest(t *testing.T) {
	for _, tc := range []struct{ line, entry, reason, err string }{
		{line: "Registry.NPMjs.org\tinstall deps\n", entry: "registry.npmjs.org", reason: "install deps"},
		{line: "*.pypi.org:443\n", entry: "*.pypi.org:443"},
		{line: "example.com\t\x1b[31mred\n", entry: "example.com", reason: "\x1b[31mred"},
		{line: "192.168.1.1\tlan\n", err: "only names"},
		{line: "[2001:db8::/32]\n", err: "only names"},
		{line: "*.com\n", err: "too broad"},
		{line: "*.co.uk\n", err: "too broad"},
		{line: "*.github.io\n", err: "too broad"},
		{line: "*.example.co.uk\n", entry: "*.example.co.uk"},
		{line: "not a name!\n", err: "invalid"},
	} {
		r, err := parseRequest(tc.line)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: err = %v, want %q", tc.line, err, tc.err)
			}
			continue
		}
		if err != nil || r.Entry != tc.entry || r.Reason != tc.reason {
			t.Errorf("%q = %q, %q, %v; want %q, %q", tc.line, r.Entry, r.Reason, err, tc.entry, tc.reason)
		}
	}
	long, _ := parseRequest("a.example\t" + strings.Repeat("é", 500))
	if n := len([]rune(long.Reason)); n != maxReasonRunes+1 {
		t.Errorf("long reason kept %d runes", n)
	}
}

func TestRequestQueue(t *testing.T) {
	var q requestQueue
	a1, _ := q.add(control.NetRequest{Entry: "a.example"})
	a2, _ := q.add(control.NetRequest{Entry: "a.example"})
	b, _ := q.add(control.NetRequest{Entry: "b.example"})
	if len(q.list()) != 2 {
		t.Fatalf("list=%v", q.list())
	}
	q.answer(map[string]string{"a.example": control.AnswerAllowed, "unknown.example": control.AnswerDenied})
	if got := <-a1; got != control.AnswerAllowed {
		t.Errorf("first waiter got %q", got)
	}
	if got := <-a2; got != control.AnswerAllowed {
		t.Errorf("second waiter got %q", got)
	}
	select {
	case got := <-b:
		t.Errorf("unanswered request got %q", got)
	default:
	}
	if l := q.list(); len(l) != 1 || l[0].Entry != "b.example" {
		t.Errorf("left pending: %v", l)
	}

	// Waiters beyond the limit (likely interrupted processes) push out the
	// oldest instead of being refused.
	first, _ := q.add(control.NetRequest{Entry: "b.example"})
	for range maxWaiters {
		if _, err := q.add(control.NetRequest{Entry: "b.example"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := <-b; ok {
		t.Error("oldest waiter not dropped")
	}
	if _, ok := <-first; ok {
		t.Error("second-oldest waiter not dropped")
	}

	for i := len(q.list()); i < maxPendingRequests; i++ {
		if _, err := q.add(control.NetRequest{Entry: fmt.Sprintf("n%d.example", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.add(control.NetRequest{Entry: "full.example"}); err == nil {
		t.Error("queue accepted more than the maximum")
	}
}

func TestToNotify(t *testing.T) {
	var q requestQueue
	t0 := time.Now()
	at := func(d time.Duration) int { return len(q.toNotify(t0.Add(d), time.Minute)) }

	q.add(control.NetRequest{Entry: "a.example"})
	if n := at(0); n != 1 {
		t.Errorf("new request: notified about %d, want 1", n)
	}
	if n := at(3 * time.Second); n != 0 {
		t.Errorf("nothing new: notified about %d", n)
	}
	q.add(control.NetRequest{Entry: "b.example"})
	q.add(control.NetRequest{Entry: "b.example"}) // joins, nothing new
	if n := at(6 * time.Second); n != 2 {
		t.Errorf("second request: notified about %d, want 2", n)
	}
	if n := at(time.Minute); n != 0 {
		t.Errorf("reminder too early: notified about %d", n)
	}
	if n := at(6*time.Second + time.Minute); n != 2 {
		t.Errorf("reminder: notified about %d, want 2", n)
	}
	q.answer(map[string]string{"a.example": "denied", "b.example": "denied"})
	if n := at(time.Hour); n != 0 {
		t.Errorf("all answered: notified about %d", n)
	}

	q.add(control.NetRequest{Entry: "c.example"})
	if n := len(q.toNotify(t0, 0)); n != 1 {
		t.Errorf("without reminders, new request: notified about %d, want 1", n)
	}
	if n := len(q.toNotify(t0.Add(time.Hour), 0)); n != 0 {
		t.Errorf("reminder with reminders off: notified about %d", n)
	}
}

func TestHandleRequest(t *testing.T) {
	d := &Daemon{log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()

	ask := func(line string) *bufio.Reader {
		guest, host := net.Pipe()
		go d.handleRequest(ctx, host)
		go fmt.Fprint(guest, line)
		return bufio.NewReader(guest)
	}
	readLine := func(r *bufio.Reader) string {
		s, _ := r.ReadString('\n')
		return s
	}

	if got := readLine(ask("10.0.0.1\n")); !strings.HasPrefix(got, "error: only names") {
		t.Errorf("address request: %q", got)
	}
	if got := readLine(ask("a.example\t" + strings.Repeat("x", maxRequestLine) + "\n")); got != "error: request too long\n" {
		t.Errorf("long request: %q", got)
	}

	r := ask("example.com\tbuild\n")
	if got := readLine(r); got != "pending\n" {
		t.Fatalf("got %q, want pending", got)
	}
	if l := d.requests.list(); len(l) != 1 || l[0].Reason != "build" {
		t.Fatalf("queue: %v", l)
	}
	d.requests.answer(map[string]string{"example.com": control.AnswerDenied})
	if got := readLine(r); got != "denied\n" {
		t.Errorf("answer: %q", got)
	}
}
