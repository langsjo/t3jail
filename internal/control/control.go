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
	"net/netip"
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
	GuestReady bool   `json:"guestReady"`
	CPUs       int    `json:"cpus"`
	MaxCPUs    int    `json:"maxCpus"`
	Memory     uint64 `json:"memory"`
	MaxMemory  uint64 `json:"maxMemory"`
	BootMemory uint64 `json:"bootMemory"`
	T3URL      string `json:"t3Url"`
	// T3Listen lists every host address forwarded to T3, as ip:port.
	T3Listen []string  `json:"t3Listen"`
	Started  time.Time `json:"started"`
}

// ResizeRequest changes the running VM's size; zero leaves a value as it is.
type ResizeRequest struct {
	CPUs   int    `json:"cpus"`
	Memory uint64 `json:"memory"`
}

// DiskResizeRequest grows the running VM's disk to Size bytes.
type DiskResizeRequest struct {
	Size uint64 `json:"size"`
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

// NetRequest is a guest's request to allow a destination (t3jail-request).
// Entry and Reason come from the guest: escape them before printing.
type NetRequest struct {
	Entry  string    `json:"entry"`
	Reason string    `json:"reason"`
	Time   time.Time `json:"time"`
}

// Answers sent back to a waiting t3jail-request.
const (
	AnswerAllowed     = "allowed"
	AnswerAllowedPerm = "allowed permanently"
	AnswerDenied      = "denied"
	AnswerBlocked     = "blocked"
	// These start with "allowed": t3jail-request matches on that prefix.
	AnswerAlreadyAllowed = "allowed already"
	AnswerAllowlistOff   = "allowed: the allowlist is off"
)

// AnswerNetRequestsRequest answers pending requests, by entry. Entries
// without a pending request are ignored.
type AnswerNetRequestsRequest struct {
	Answers map[string]string `json:"answers"`
}

// PortDirection says which side of a port forward listens.
type PortDirection string

const (
	// PortToVM listens on the host and connects to the VM.
	PortToVM PortDirection = "to"
	// PortFromVM listens in the VM and connects to the host.
	PortFromVM PortDirection = "from"
)

// PortForward forwards a TCP port between the host and the VM. Listen is on
// one side, Target (on localhost) on the other. A forward is identified by
// Direction and Listen.
type PortForward struct {
	Direction PortDirection `toml:"direction" json:"direction"`
	// Bind is the host address a PortToVM forward listens on.
	Bind   netip.Addr `toml:"bind,omitempty" json:"bind,omitzero"`
	Listen uint16     `toml:"listen" json:"listen"`
	Target uint16     `toml:"target" json:"target"`
}

// DefaultPortBind is where PortToVM forwards listen unless Bind says otherwise.
var DefaultPortBind = netip.AddrFrom4([4]byte{127, 0, 0, 1})

// Normalized checks f and fills in the default bind address.
func (f PortForward) Normalized() (PortForward, error) {
	if f.Listen == 0 || f.Target == 0 {
		return f, errors.New("port 0")
	}
	switch f.Direction {
	case PortToVM:
		if !f.Bind.IsValid() {
			f.Bind = DefaultPortBind
		}
		f.Bind = f.Bind.Unmap()
	case PortFromVM:
		if f.Bind.IsValid() {
			return f, errors.New("a bind address applies only to forwards to the VM")
		}
	default:
		return f, fmt.Errorf("unknown direction %q", f.Direction)
	}
	return f, nil
}

func (f PortForward) String() string {
	if f.Direction == PortFromVM {
		return fmt.Sprintf("VM localhost:%d -> host localhost:%d", f.Listen, f.Target)
	}
	bind := f.Bind
	if !bind.IsValid() {
		bind = DefaultPortBind
	}
	return fmt.Sprintf("host %s -> VM localhost:%d", netip.AddrPortFrom(bind, f.Listen), f.Target)
}

// ClosePortRequest closes the forward listening on Listen.
type ClosePortRequest struct {
	Direction PortDirection `json:"direction"`
	Listen    uint16        `json:"listen"`
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

func (c *Client) Resize(ctx context.Context, req ResizeRequest) error {
	return c.Call(ctx, http.MethodPost, "/v1/resize", req, nil)
}

func (c *Client) ResizeDisk(ctx context.Context, req DiskResizeRequest) error {
	return c.Call(ctx, http.MethodPost, "/v1/disk/resize", req, nil)
}

// ReloadNetwork makes the running VM use the current network.list.
func (c *Client) ReloadNetwork(ctx context.Context) error {
	return c.Call(ctx, http.MethodPost, "/v1/net/reload", nil, nil)
}

func (c *Client) NetRequests(ctx context.Context) ([]NetRequest, error) {
	var out []NetRequest
	err := c.Call(ctx, http.MethodGet, "/v1/net/requests", nil, &out)
	return out, err
}

func (c *Client) AnswerNetRequests(ctx context.Context, answers map[string]string) error {
	return c.Call(ctx, http.MethodPost, "/v1/net/requests/answer", AnswerNetRequestsRequest{Answers: answers}, nil)
}

// OpenPort starts a forward and returns it with defaults filled in.
func (c *Client) OpenPort(ctx context.Context, f PortForward) (PortForward, error) {
	var out PortForward
	err := c.Call(ctx, http.MethodPost, "/v1/ports", f, &out)
	return out, err
}

func (c *Client) ClosePort(ctx context.Context, req ClosePortRequest) error {
	return c.Call(ctx, http.MethodPost, "/v1/ports/close", req, nil)
}

func (c *Client) Ports(ctx context.Context) ([]PortForward, error) {
	var out []PortForward
	err := c.Call(ctx, http.MethodGet, "/v1/ports", nil, &out)
	return out, err
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
