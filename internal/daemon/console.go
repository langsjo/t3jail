package daemon

import (
	"io"
	"log/slog"
	"net"
	"sync"
)

// serialMux connects to cloud-hypervisor's serial socket once and fans the
// output out to a log file and any attached `t3jail console` clients. Input
// from clients goes to the guest's serial port.
type serialMux struct {
	serial net.Conn
	log    io.Writer
	logger *slog.Logger

	mu      sync.Mutex
	clients map[net.Conn]struct{}
}

func newSerialMux(serial net.Conn, log io.Writer, logger *slog.Logger) *serialMux {
	return &serialMux{serial: serial, log: log, logger: logger, clients: map[net.Conn]struct{}{}}
}

// pump copies guest output until the serial connection closes.
func (m *serialMux) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := m.serial.Read(buf)
		if n > 0 {
			m.log.Write(buf[:n])
			m.mu.Lock()
			for c := range m.clients {
				if _, werr := c.Write(buf[:n]); werr != nil {
					c.Close()
					delete(m.clients, c)
				}
			}
			m.mu.Unlock()
		}
		if err != nil {
			m.closeClients()
			return
		}
	}
}

// serve attaches every accepted connection as a console client.
func (m *serialMux) serve(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		m.mu.Lock()
		m.clients[c] = struct{}{}
		m.mu.Unlock()
		go func() {
			io.Copy(m.serial, c)
			m.mu.Lock()
			delete(m.clients, c)
			m.mu.Unlock()
			c.Close()
		}()
	}
}

func (m *serialMux) closeClients() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for c := range m.clients {
		c.Close()
	}
	clear(m.clients)
}
