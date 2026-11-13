package daemon

// Network requests: a process in the guest (t3jail-request) asks the user to
// allow a destination. It connects over vsock; the user answers with
// `t3jail net requests` or `t3jail net allow`.
//
// Protocol, lines both ways: the guest sends "ENTRY\tREASON\n"; the daemon
// replies "pending\n" (or "error: ...\n" and closes), then, once the user
// decides, one of the control.Answer* values and closes. If the entry is
// already allowed, or the allowlist is off, it answers at once instead.
// Everything the guest sends is untrusted.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/netrules"
	"github.com/langsjo/t3jail/internal/termsafe"
	"golang.org/x/net/publicsuffix"
)

const (
	// Bound what the guest can make the daemon hold and the user read.
	maxPendingRequests = 20
	maxWaiters         = 16 // connections waiting on one request
	maxReasonRunes     = 200
	// A name (at most 253 bytes), a tab and a reason of maxReasonRunes
	// (t3jail-request cuts it to that) fit with room to spare.
	maxRequestLine = 1100
)

const notifyTick = 3 * time.Second

type requestQueue struct {
	mu      sync.Mutex
	pending []*pendingRequest
	// fresh is set when a request arrives after the last notification, at
	// notified.
	fresh    bool
	notified time.Time
}

type pendingRequest struct {
	control.NetRequest
	waiters []chan string
}

// add queues r, or joins the pending request for the same entry. The channel
// receives the answer, or is closed if the waiter is dropped.
func (q *requestQueue) add(r control.NetRequest) (answer <-chan string, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ch := make(chan string, 1)
	for _, p := range q.pending {
		if p.Entry == r.Entry {
			// A waiter whose process was interrupted is not noticed (the
			// guest half-closes its side right after the request), so drop
			// the oldest rather than refuse new ones.
			if len(p.waiters) >= maxWaiters {
				close(p.waiters[0])
				p.waiters = p.waiters[1:]
			}
			p.waiters = append(p.waiters, ch)
			return ch, nil
		}
	}
	if len(q.pending) >= maxPendingRequests {
		return nil, errors.New("too many pending requests; wait for the user to answer them")
	}
	q.pending = append(q.pending, &pendingRequest{NetRequest: r, waiters: []chan string{ch}})
	q.fresh = true
	return ch, nil
}

func (q *requestQueue) list() []control.NetRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.listLocked()
}

// toNotify returns the pending requests if the user should be told now: some
// arrived since the last notification, or reminder (if not 0) has passed.
func (q *requestQueue) toNotify(now time.Time, reminder time.Duration) []control.NetRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	due := q.fresh || reminder > 0 && now.Sub(q.notified) >= reminder
	q.fresh = false
	if !due || len(q.pending) == 0 {
		return nil
	}
	q.notified = now
	return q.listLocked()
}

func (q *requestQueue) listLocked() []control.NetRequest {
	out := []control.NetRequest{}
	for _, p := range q.pending {
		out = append(out, p.NetRequest)
	}
	return out
}

// answer resolves the pending requests named in answers.
func (q *requestQueue) answer(answers map[string]string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.pending[:0]
	for _, p := range q.pending {
		a, ok := answers[p.Entry]
		if !ok {
			kept = append(kept, p)
			continue
		}
		for _, ch := range p.waiters {
			ch <- a
		}
	}
	q.pending = kept
}

// parseRequest validates a request line. Only names can be requested:
// address entries override the block of private and host networks, which an
// agent should not get with one keypress.
func parseRequest(line string) (control.NetRequest, error) {
	entryStr, reason, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "\t")
	e, err := netrules.Parse(entryStr)
	if err != nil {
		return control.NetRequest{}, err
	}
	if e.Kind == netrules.KindPrefix {
		return control.NetRequest{}, errors.New("only names can be requested, not addresses")
	}
	// "*.co.uk" or "*.github.io" would cover sites of unrelated owners.
	if _, err := publicsuffix.EffectiveTLDPlusOne(e.Name); e.Kind == netrules.KindWildcard && err != nil {
		return control.NetRequest{}, fmt.Errorf("*.%s is too broad", e.Name)
	}
	reason = strings.TrimSpace(strings.ToValidUTF8(reason, "?"))
	if r := []rune(reason); len(r) > maxReasonRunes {
		reason = string(r[:maxReasonRunes]) + "…"
	}
	return control.NetRequest{Entry: e.String(), Reason: reason, Time: time.Now()}, nil
}

// answerNow returns the answer for an entry that needs no decision, or "".
func (d *Daemon) answerNow(entry string) string {
	// The daemon's own VM is running, so the override applies.
	if state, err := netrules.LoadFilterState(d.inst.NetworkOffMarker(), d.inst.NetworkFilterOverride()); err == nil && state.Off {
		return control.AnswerAllowlistOff
	}
	if listsCover([]string{d.inst.NetworkList(), d.inst.EphemeralNetworkList()}, entry) {
		return control.AnswerAlreadyAllowed
	}
	return ""
}

// listsCover reports whether an entry in the allowlist files covers entry.
// Missing or unreadable files cover nothing: the request then goes to the user.
func listsCover(files []string, entry string) bool {
	e, err := netrules.Parse(entry)
	if err != nil {
		return false
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		list, err := netrules.ParseList(data)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(list.Entries(), func(a netrules.Entry) bool { return a.Covers(e) }) {
			return true
		}
	}
	return false
}

// serveRequests accepts the guest's connections to the request port.
func (d *Daemon) serveRequests(ctx context.Context) error {
	l, err := listenUnix(d.inst.VsockHostSocket(d.abi.VsockRequestPort))
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	go d.notifyLoop(ctx)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go d.handleRequest(ctx, conn)
		}
	}()
	return nil
}

func (d *Daemon) handleRequest(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(conn, maxRequestLine)).ReadString('\n')
	if err != nil {
		if len(line) >= maxRequestLine {
			io.WriteString(conn, "error: request too long\n")
		}
		return
	}
	req, err := parseRequest(line)
	if err == nil {
		if a := d.answerNow(req.Entry); a != "" {
			fmt.Fprintf(conn, "%s\n", a)
			return
		}
	}
	var answer <-chan string
	if err == nil {
		answer, err = d.requests.add(req)
	}
	if err != nil {
		fmt.Fprintf(conn, "error: %v\n", err)
		return
	}
	if _, err := io.WriteString(conn, "pending\n"); err != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	d.log.Info("network request", "entry", req.Entry, "reason", req.Reason)
	select {
	case a, ok := <-answer:
		if ok {
			fmt.Fprintf(conn, "%s\n", a)
		}
	case <-ctx.Done():
	}
}

// notifyLoop checks every notifyTick whether to show a notification. The
// tick also coalesces a burst of requests into one notification.
func (d *Daemon) notifyLoop(ctx context.Context) {
	tick := time.NewTicker(notifyTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if reqs := d.requests.toNotify(now, d.spec.RequestReminder); reqs != nil {
				go notify(ctx, reqs)
			}
		}
	}
}

// notify shows a desktop notification if notify-send is installed.
func notify(ctx context.Context, reqs []control.NetRequest) {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var summary, body string
	if len(reqs) == 1 {
		summary = "t3jail: the VM asks to reach " + reqs[0].Entry
		// The body may be rendered as markup.
		body = html.EscapeString(string(termsafe.Escape([]byte(reqs[0].Reason))))
	} else {
		summary = fmt.Sprintf("t3jail: %d network requests pending", len(reqs))
		entries := make([]string, len(reqs))
		for i, r := range reqs {
			entries[i] = r.Entry // validated names: no markup
		}
		body = strings.Join(entries, ", ")
	}
	if body != "" {
		body += "\n\n"
	}
	body += "Answer with: t3jail net requests"
	exec.CommandContext(ctx, path, "--app-name=t3jail", summary, body).Run()
}

func (d *Daemon) listRequests(context.Context, control.Empty) ([]control.NetRequest, error) {
	return d.requests.list(), nil
}

func (d *Daemon) answerRequests(_ context.Context, req control.AnswerNetRequestsRequest) (control.Empty, error) {
	d.requests.answer(req.Answers)
	return control.Empty{}, nil
}
