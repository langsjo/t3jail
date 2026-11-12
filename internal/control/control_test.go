package control

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "control.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var gotStop StopRequest
	mux := http.NewServeMux()
	mux.Handle("GET /v1/status", Handler(func(context.Context, Empty) (Status, error) {
		return Status{State: StateRunning, CPUs: 4, GuestReady: true}, nil
	}))
	mux.Handle("POST /v1/stop", Handler(func(_ context.Context, req StopRequest) (Empty, error) {
		gotStop = req
		return Empty{}, errors.New("guest did not stop")
	}))
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	defer srv.Close()

	c := NewClient(sock)
	st, err := c.Status(context.Background())
	if err != nil || st.State != StateRunning || st.CPUs != 4 || !st.GuestReady {
		t.Fatalf("status = %+v, %v", st, err)
	}
	err = c.Stop(context.Background(), StopRequest{Force: true, Timeout: time.Minute})
	if err == nil || err.Error() != "guest did not stop" {
		t.Fatalf("stop error = %v, want the daemon's message", err)
	}
	if !gotStop.Force || gotStop.Timeout != time.Minute {
		t.Fatalf("daemon got %+v", gotStop)
	}
}

func TestNotRunning(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}
