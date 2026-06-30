// Package gomc implements Mitsubishi MC Protocol clients for 3E and 4E frames
// over TCP and UDP transports.
package gomc

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client3E is a 3E frame MC Protocol client (TCP or UDP).
// Safe for concurrent use; requests are serialized by an internal mutex.
type Client3E struct {
	mu      sync.Mutex
	host    string
	port    int
	mode    Mode
	timeout time.Duration
	timer   uint16
	isUDP   bool
	conn    net.Conn
}

// New3EClient creates a new 3E frame client. Call Connect before use.
func New3EClient(host string, port int, mode Mode) (*Client3E, error) {
	if mode != ModeBinary && mode != ModeASCII {
		return nil, fmt.Errorf("mode must be ModeBinary or ModeASCII")
	}
	return &Client3E{
		host:    host,
		port:    port,
		mode:    mode,
		timeout: 5 * time.Second,
		timer:   0x0010,
	}, nil
}

// New3EClientUDP creates a new 3E frame client using UDP transport.
// Call Connect before use.
func New3EClientUDP(host string, port int, mode Mode) (*Client3E, error) {
	c, err := New3EClient(host, port, mode)
	if err != nil {
		return nil, err
	}
	c.isUDP = true
	return c, nil
}

// Connect establishes the connection to the PLC (TCP or UDP).
func (c *Client3E) Connect() error {
	proto := "tcp"
	if c.isUDP {
		proto = "udp"
	}
	c.mu.Lock()
	timeout := c.timeout
	c.mu.Unlock()
	conn, err := net.DialTimeout(proto, fmt.Sprintf("%s:%d", c.host, c.port), timeout)
	if err != nil {
		return &ConnectionError{msg: "connect: " + err.Error()}
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return nil
}

// sendBin sends a binary frame and returns the response.
// Acquires the mutex to serialize concurrent callers.
func (c *Client3E) sendBin(frame []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, newConnError("not connected")
	}
	c.applyDeadline()
	if c.isUDP {
		return xferBinUDP(c.conn, frame)
	}
	return xferBin(c.conn, frame)
}

// sendAsc sends an ASCII frame and returns the response.
// Acquires the mutex to serialize concurrent callers.
func (c *Client3E) sendAsc(frame string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return "", newConnError("not connected")
	}
	c.applyDeadline()
	if c.isUDP {
		return xferAscUDP(c.conn, frame)
	}
	return xferAsc(c.conn, frame)
}

// SetTimeout sets the per-request I/O deadline and the connect timeout.
// A value of 0 or less disables both the per-request deadline and the
// connect timeout (Connect will block until the OS times out).
// Default is 5 seconds.
func (c *Client3E) SetTimeout(d time.Duration) {
	c.mu.Lock()
	c.timeout = d
	c.mu.Unlock()
}

// applyDeadline sets or clears the connection deadline.
// Must be called with c.mu held.
func (c *Client3E) applyDeadline() {
	if c.timeout > 0 {
		c.conn.SetDeadline(time.Now().Add(c.timeout))
	} else {
		c.conn.SetDeadline(time.Time{})
	}
}

// Close closes the connection.
func (c *Client3E) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func (c *Client3E) validate(device string, start, count int) (string, error) {
	dev := strings.ToUpper(device)
	if _, ok := binCode[dev]; !ok {
		return "", fmt.Errorf("unsupported device %q", device)
	}
	if start < 0 {
		return "", fmt.Errorf("start must be >= 0, got %d", start)
	}
	if count <= 0 {
		return "", fmt.Errorf("count must be > 0, got %d", count)
	}
	return dev, nil
}

func (c *Client3E) binBody(dev string, start, count int) []byte {
	b := make([]byte, 6)
	copy(b, addrBin(dev, start))
	binary.LittleEndian.PutUint16(b[4:], uint16(count))
	return b
}

// ReadWords reads count word values from device starting at address start.
func (c *Client3E) ReadWords(device string, start, count int) ([]uint16, error) {
	dev, err := c.validate(device, start, count)
	if err != nil {
		return nil, err
	}
	if c.mode == ModeBinary {
		resp, err := c.sendBin(buildBin(c.timer, cmdRead, subcWord, c.binBody(dev, start, count)))
		if err != nil {
			return nil, err
		}
		raw, err := chkBin(resp)
		if err != nil {
			return nil, err
		}
		if len(raw) < count*2 {
			return nil, newConnError(fmt.Sprintf("short payload: expected %d bytes, got %d", count*2, len(raw)))
		}
		vals := make([]uint16, count)
		for i := range vals {
			vals[i] = binary.LittleEndian.Uint16(raw[i*2:])
		}
		return vals, nil
	}
	body := addrAsc(dev, start) + fmt.Sprintf("%04X", count)
	resp, err := c.sendAsc(buildAsc(c.timer, cmdRead, subcWord, body))
	if err != nil {
		return nil, err
	}
	raw, err := chkAsc(resp)
	if err != nil {
		return nil, err
	}
	if len(raw) < count*4 {
		return nil, newConnError(fmt.Sprintf("short payload: expected %d chars, got %d", count*4, len(raw)))
	}
	vals := make([]uint16, count)
	for i := range vals {
		v, err := strconv.ParseUint(raw[i*4:(i+1)*4], 16, 16)
		if err != nil {
			return nil, newConnError(fmt.Sprintf("invalid word at index %d: %v", i, err))
		}
		vals[i] = uint16(v)
	}
	return vals, nil
}

// WriteWords writes values to device starting at address start.
func (c *Client3E) WriteWords(device string, start int, values []uint16) error {
	dev, err := c.validate(device, start, len(values))
	if err != nil {
		return err
	}
	if c.mode == ModeBinary {
		wbuf := make([]byte, len(values)*2)
		for i, v := range values {
			binary.LittleEndian.PutUint16(wbuf[i*2:], v)
		}
		body := append(c.binBody(dev, start, len(values)), wbuf...)
		resp, err := c.sendBin(buildBin(c.timer, cmdWrite, subcWord, body))
		if err != nil {
			return err
		}
		_, err = chkBin(resp)
		return err
	}
	prefix := addrAsc(dev, start) + fmt.Sprintf("%04X", len(values))
	var sb strings.Builder
	sb.Grow(len(prefix) + len(values)*4)
	sb.WriteString(prefix)
	for _, v := range values {
		sb.WriteString(fmt.Sprintf("%04X", v))
	}
	resp, err := c.sendAsc(buildAsc(c.timer, cmdWrite, subcWord, sb.String()))
	if err != nil {
		return err
	}
	_, err = chkAsc(resp)
	return err
}

// ReadBits reads count bit values from device starting at address start.
func (c *Client3E) ReadBits(device string, start, count int) ([]bool, error) {
	dev, err := c.validate(device, start, count)
	if err != nil {
		return nil, err
	}
	if c.mode == ModeBinary {
		resp, err := c.sendBin(buildBin(c.timer, cmdRead, subcBit, c.binBody(dev, start, count)))
		if err != nil {
			return nil, err
		}
		raw, err := chkBin(resp)
		if err != nil {
			return nil, err
		}
		if expected := (count + 1) / 2; len(raw) < expected {
			return nil, newConnError(fmt.Sprintf("short payload: expected %d bytes, got %d", expected, len(raw)))
		}
		bits := make([]bool, count)
		for i := range bits {
			b := raw[i/2]
			if i%2 == 0 {
				bits[i] = (b>>4)&0x01 != 0
			} else {
				bits[i] = b&0x01 != 0
			}
		}
		return bits, nil
	}
	body := addrAsc(dev, start) + fmt.Sprintf("%04X", count)
	resp, err := c.sendAsc(buildAsc(c.timer, cmdRead, subcBit, body))
	if err != nil {
		return nil, err
	}
	raw, err := chkAsc(resp)
	if err != nil {
		return nil, err
	}
	if len(raw) < count {
		return nil, newConnError(fmt.Sprintf("short payload: expected %d chars, got %d", count, len(raw)))
	}
	bits := make([]bool, count)
	for i := range bits {
		bits[i] = raw[i] == '1'
	}
	return bits, nil
}

// WriteBits writes bit values to device starting at address start.
func (c *Client3E) WriteBits(device string, start int, values []bool) error {
	dev, err := c.validate(device, start, len(values))
	if err != nil {
		return err
	}
	if c.mode == ModeBinary {
		buf := make([]byte, (len(values)+1)/2)
		for i, v := range values {
			if v {
				if i%2 == 0 {
					buf[i/2] |= 0x10
				} else {
					buf[i/2] |= 0x01
				}
			}
		}
		body := append(c.binBody(dev, start, len(values)), buf...)
		resp, err := c.sendBin(buildBin(c.timer, cmdWrite, subcBit, body))
		if err != nil {
			return err
		}
		_, err = chkBin(resp)
		return err
	}
	prefix := addrAsc(dev, start) + fmt.Sprintf("%04X", len(values))
	var sb strings.Builder
	sb.Grow(len(prefix) + len(values))
	sb.WriteString(prefix)
	for _, v := range values {
		if v {
			sb.WriteByte('1')
		} else {
			sb.WriteByte('0')
		}
	}
	resp, err := c.sendAsc(buildAsc(c.timer, cmdWrite, subcBit, sb.String()))
	if err != nil {
		return err
	}
	_, err = chkAsc(resp)
	return err
}

// ---- Typed Read/Write Helpers ----

// ReadBool reads a single bit from a bit device (X, Y, M, etc.).
func (c *Client3E) ReadBool(device string, start int) (bool, error) {
	bits, err := c.ReadBits(device, start, 1)
	if err != nil {
		return false, err
	}
	return bits[0], nil
}

// ReadInt16 reads a single 16-bit signed integer (1 word).
func (c *Client3E) ReadInt16(device string, start int) (int16, error) {
	words, err := c.ReadWords(device, start, 1)
	if err != nil {
		return 0, err
	}
	return int16(words[0]), nil
}

// ReadUInt16 reads a single 16-bit unsigned integer (1 word).
func (c *Client3E) ReadUInt16(device string, start int) (uint16, error) {
	words, err := c.ReadWords(device, start, 1)
	if err != nil {
		return 0, err
	}
	return words[0], nil
}

// ReadInt32 reads a single 32-bit signed integer (2 words, little-endian).
func (c *Client3E) ReadInt32(device string, start int) (int32, error) {
	words, err := c.ReadWords(device, start, 2)
	if err != nil {
		return 0, err
	}
	return int32(uint32(words[0]) | uint32(words[1])<<16), nil
}

// ReadUInt32 reads a single 32-bit unsigned integer (2 words).
func (c *Client3E) ReadUInt32(device string, start int) (uint32, error) {
	words, err := c.ReadWords(device, start, 2)
	if err != nil {
		return 0, err
	}
	return uint32(words[0]) | uint32(words[1])<<16, nil
}

// ReadFloat32 reads a single 32-bit float (2 words).
func (c *Client3E) ReadFloat32(device string, start int) (float32, error) {
	v, err := c.ReadUInt32(device, start)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(v), nil
}

// ReadInt64 reads a single 64-bit signed integer (4 words).
func (c *Client3E) ReadInt64(device string, start int) (int64, error) {
	words, err := c.ReadWords(device, start, 4)
	if err != nil {
		return 0, err
	}
	v := uint64(words[0]) | uint64(words[1])<<16 | uint64(words[2])<<32 | uint64(words[3])<<48
	return int64(v), nil
}

// ReadUInt64 reads a single 64-bit unsigned integer (4 words).
func (c *Client3E) ReadUInt64(device string, start int) (uint64, error) {
	words, err := c.ReadWords(device, start, 4)
	if err != nil {
		return 0, err
	}
	return uint64(words[0]) | uint64(words[1])<<16 | uint64(words[2])<<32 | uint64(words[3])<<48, nil
}

// ReadFloat64 reads a single 64-bit float (4 words).
func (c *Client3E) ReadFloat64(device string, start int) (float64, error) {
	v, err := c.ReadUInt64(device, start)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}

// WriteValue writes a typed value to a device at start.
// Supported types: bool, int8, uint8, int16, uint16, int32, uint32, float32, int64, uint64, float64, string.
func (c *Client3E) WriteValue(device string, start int, value any) error {
	switch v := value.(type) {
	case bool:
		return c.WriteBits(device, start, []bool{v})
	case int8:
		return c.WriteWords(device, start, []uint16{uint16(v)})
	case uint8:
		return c.WriteWords(device, start, []uint16{uint16(v)})
	case int16:
		return c.WriteWords(device, start, []uint16{uint16(v)})
	case uint16:
		return c.WriteWords(device, start, []uint16{v})
	case int32:
		u := uint32(v)
		return c.WriteWords(device, start, []uint16{uint16(u), uint16(u >> 16)})
	case uint32:
		return c.WriteWords(device, start, []uint16{uint16(v), uint16(v >> 16)})
	case float32:
		return c.WriteWords(device, start, []uint16{uint16(math.Float32bits(v)), uint16(math.Float32bits(v) >> 16)})
	case int64:
		u := uint64(v)
		return c.WriteWords(device, start, []uint16{uint16(u), uint16(u >> 16), uint16(u >> 32), uint16(u >> 48)})
	case uint64:
		return c.WriteWords(device, start, []uint16{uint16(v), uint16(v >> 16), uint16(v >> 32), uint16(v >> 48)})
	case float64:
		u := math.Float64bits(v)
		return c.WriteWords(device, start, []uint16{uint16(u), uint16(u >> 16), uint16(u >> 32), uint16(u >> 48)})
	case string:
		buf := []byte(v)
		if len(buf)%2 != 0 {
			buf = append(buf, 0)
		}
		words := make([]uint16, len(buf)/2)
		for i := range words {
			words[i] = uint16(buf[i*2]) | uint16(buf[i*2+1])<<8
		}
		return c.WriteWords(device, start, words)
	default:
		return fmt.Errorf("unsupported type %T", v)
	}
}
