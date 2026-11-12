// Package control is the API between t3jail commands and the running daemon:
// JSON over HTTP on a Unix socket in the private runtime directory.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/langsjo/t3jail/internal/mounts"
)

// State of the VM as seen by the daemon.
type State string

const (
	StateBooting  State = "booting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
)

type Status struct {
	State  State `json:"state"`
	PID    int   `json:"pid"`
	Rescue bool  `json:"rescue"`
	// GuestReady is true once the guest's SSH server answers.
	GuestReady bool      `json:"guestReady"`
	CPUs       int       `json:"cpus"`
	MaxCPUs    int       `json:"maxCpus"`
	Memory     uint64    `json:"memory"`
	MaxMemory  uint64    `json:"maxMemory"`
	BootMemory uint64    `json:"bootMemory"`
	T3URL      string    `json:"t3Url"`
	Started    time.Time `json:"started"`
}

// RemoveMountRequest detaches the share mounted at GuestPath.
type RemoveMountRequest struct {
	GuestPath string `json:"guestPath"`
}

type StopRequest struct {
	// Force skips the clean guest shutdown.
	Force bool `json:"force"`
	// Timeout for the clean shutdown before the VM is killed.
	Timeout time.Duration `json:"timeout"`
}

// ErrNotRunning means no daemon is listening on the control socket.
var ErrNotRunning = errors.New("the VM is not running")

type Client struct {
	http *http.Client
}

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
			if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
				return nil, ErrNotRunning
			}
			return conn, err
		},
	}}}
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.Call(ctx, http.MethodGet, "/v1/status", nil, &s)
	return s, err
}

func (c *Client) Stop(ctx context.Context, req StopRequest) error {
	return c.Call(ctx, http.MethodPost, "/v1/stop", req, nil)
}

// AddMount attaches a share to the running VM and returns it with the host
// path resolved. The guest still has to mount it.
func (c *Client) AddMount(ctx context.Context, m mounts.Mount) (mounts.Mount, error) {
	var out mounts.Mount
	err := c.Call(ctx, http.MethodPost, "/v1/mounts", m, &out)
	return out, err
}

func (c *Client) RemoveMount(ctx context.Context, guestPath string) error {
	return c.Call(ctx, http.MethodPost, "/v1/mounts/remove", RemoveMountRequest{GuestPath: guestPath}, nil)
}

func (c *Client) Mounts(ctx context.Context) ([]mounts.Mount, error) {
	var out []mounts.Mount
	err := c.Call(ctx, http.MethodGet, "/v1/mounts", nil, &out)
	return out, err
}

// ReloadNetwork makes the running VM use the current network.list.
func (c *Client) ReloadNetwork(ctx context.Context) error {
	return c.Call(ctx, http.MethodPost, "/v1/net/reload", nil, nil)
}

// Call sends body (if not nil) as JSON and decodes the response into out.
func (c *Client) Call(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://t3jail"+path, reqBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if errors.Is(err, ErrNotRunning) {
		return ErrNotRunning
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e errorBody
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("daemon: %s", resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type errorBody struct {
	Error string `json:"error"`
}

// Handler adapts a typed function to an HTTP handler: decode the request body
// into In (if any), call fn, encode Out or the error as JSON.
func Handler[In, Out any](fn func(context.Context, In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in In
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, errorBody{Error: "bad request: " + err.Error()})
				return
			}
		}
		out, err := fn(r.Context(), in)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// Empty is used for requests or responses without a body.
type Empty struct{}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
