package chv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client talks to cloud-hypervisor's REST API on its Unix socket. Only the
// endpoints t3jail needs are wrapped; see vmm/src/api/openapi/cloud-hypervisor.yaml.
type Client struct {
	http *http.Client
}

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}}
}

type VMInfo struct {
	State            string   `json:"state"`
	MemoryActualSize uint64   `json:"memory_actual_size"`
	Config           VMConfig `json:"config"`
}

type VMConfig struct {
	CPUs struct {
		BootVCPUs int `json:"boot_vcpus"`
		MaxVCPUs  int `json:"max_vcpus"`
	} `json:"cpus"`
	Memory struct {
		Size           uint64 `json:"size"`
		HotplugSize    uint64 `json:"hotplug_size"`
		HotpluggedSize uint64 `json:"hotplugged_size"`
	} `json:"memory"`
	Fs []struct {
		Tag string `json:"tag"`
		ID  string `json:"id"`
	} `json:"fs"`
}

func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "vmm.ping", nil, nil)
}

func (c *Client) Info(ctx context.Context) (VMInfo, error) {
	var info VMInfo
	err := c.do(ctx, http.MethodGet, "vm.info", nil, &info)
	return info, err
}

// PowerButton asks the guest to shut down cleanly (ACPI).
func (c *Client) PowerButton(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.power-button", nil, nil)
}

// ShutdownVMM stops the VM and the cloud-hypervisor process immediately.
func (c *Client) ShutdownVMM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vmm.shutdown", nil, nil)
}

// Resize sets the vCPU count and/or total RAM; zero leaves a value unchanged.
func (c *Client) Resize(ctx context.Context, vcpus int, ram uint64) error {
	body := map[string]any{}
	if vcpus > 0 {
		body["desired_vcpus"] = vcpus
	}
	if ram > 0 {
		body["desired_ram"] = ram
	}
	return c.do(ctx, http.MethodPut, "vm.resize", body, nil)
}

func (c *Client) AddFs(ctx context.Context, sh Share) error {
	body := map[string]any{
		"tag": sh.Tag, "socket": sh.Socket, "id": sh.ID,
		"num_queues": 1, "queue_size": FsQueueSize,
	}
	return c.do(ctx, http.MethodPut, "vm.add-fs", body, nil)
}

func (c *Client) RemoveDevice(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPut, "vm.remove-device", map[string]string{"id": id}, nil)
}

func (c *Client) do(ctx context.Context, method, endpoint string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost/api/v1/"+endpoint, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cloud-hypervisor %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cloud-hypervisor %s: %s: %s", endpoint, resp.Status, bytes.TrimSpace(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("cloud-hypervisor %s: decoding response: %w", endpoint, err)
		}
	}
	return nil
}
