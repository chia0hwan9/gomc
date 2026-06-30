package gomc

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"math"
	"time"
)

// Client4E is a 4E frame MC Protocol client (TCP only).
// The 4E frame extends 3E by adding a serial number and reserved field.
// Safe for concurrent use; requests are serialized by an internal mutex.
type Client4E struct {
	mu       sync.Mutex
	host     string
	port     int
	mode     Mode
	timeout  time.Duration
	timer    uint16
	serialNo uint16
	conn     net.Conn
}

// New4EClient creates a new 4E frame client. Call Connect before use.
func New4EClient(host string, port int, mode Mode) (*Client4E, error) {
	if mode != ModeBinary && mode != ModeASCII {
		return nil, fmt.Errorf("mode must be ModeBinary or ModeASCII")
	}
	return &Client4E{
		host:    host,
		port:    port,
		mode:    mode,
		timeout: 5 * time.Second,
		timer:   0x0010,
	}, nil
}

// Connect establishes the TCP connection to the PLC.
func (c *Client4E) Connect() error {
	c.mu.Lock()
	timeout := c.timeout
	c.mu.Unlock()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", c.host, c.port), timeout)
	if err != nil {
		return &ConnectionError{msg: "connect: " + err.Error()}
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return nil
}

// SetTimeout sets the per-request I/O deadline and the connect timeout.
// A value of 0 or less disables both the per-request deadline and the
// connect timeout (Connect will block until the OS times out).
// Default is 5 seconds.
func (c *Client4E) SetTimeout(d time.Duration) {
	c.mu.Lock()
	c.timeout = d
	c.mu.Unlock()
}

// applyDeadline sets or clears the connection deadline.
// Must be called with c.mu held.
func (c *Client4E) applyDeadline() {
	if c.timeout > 0 {
		c.conn.SetDeadline(time.Now().Add(c.timeout))
	} else {
		c.conn.SetDeadline(time.Time{})
	}
}

// Close closes the TCP connection.
func (c *Client4E) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func (c *Client4E) nextSerial() uint16 {
	c.serialNo++
	if c.serialNo == 0 {
		c.serialNo = 1
	}
	return c.serialNo
}

// build4EBin builds a 4E binary frame.
// Layout: subheader(2) + serial(2) + reserved(2) + net(1) + pc(1) + ioNum(2) + station(1) + dataLen(2) + timer(2) + cmd(2) + subcmd(2) + body
func (c *Client4E) build4EBin(serial uint16, body []byte) []byte {
	// inner = timer(2) + body
	inner := make([]byte, 2+len(body))
	binary.LittleEndian.PutUint16(inner[0:], c.timer)
	copy(inner[2:], body)
	// frame header (13 bytes) + inner
	frame := make([]byte, 13+len(inner))
	frame[0] = 0x54
	frame[1] = 0x00
	binary.LittleEndian.PutUint16(frame[2:], serial)
	// reserved (frame[4:6]) = 0x00 0x00
	frame[6] = 0x00  // network number
	frame[7] = 0xFF  // PC number
	frame[8] = 0xFF  // IO number lo
	frame[9] = 0x03  // IO number hi
	frame[10] = 0x00 // station number
	binary.LittleEndian.PutUint16(frame[11:], uint16(len(inner)))
	copy(frame[13:], inner)
	return frame
}

// build4EAsc builds a 4E ASCII frame.
func (c *Client4E) build4EAsc(serial uint16, body string) string {
	inner := fmt.Sprintf("%04X%s", c.timer, body)
	return fmt.Sprintf("5400%04X000000FF03FF00%04X%s", serial, len(inner), inner)
}

func (c *Client4E) sendBin(cmd, subcmd uint16, body []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, newConnError("not connected")
	}
	c.applyDeadline()
	serial := c.nextSerial()
	payload := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint16(payload[0:], cmd)
	binary.LittleEndian.PutUint16(payload[2:], subcmd)
	copy(payload[4:], body)
	frame := c.build4EBin(serial, payload)
	if _, err := c.conn.Write(frame); err != nil {
		return nil, newConnError("send: " + err.Error())
	}
	hdr := make([]byte, 13)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return nil, newConnError("recv header: " + err.Error())
	}
	dataLen := int(binary.LittleEndian.Uint16(hdr[11:]))
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(c.conn, data); err != nil {
		return nil, newConnError("recv body: " + err.Error())
	}
	return append(hdr, data...), nil
}

func (c *Client4E) sendAsc(cmd, subcmd uint16, body string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return "", newConnError("not connected")
	}
	c.applyDeadline()
	serial := c.nextSerial()
	inner := fmt.Sprintf("%04X%04X%s", cmd, subcmd, body)
	frame := c.build4EAsc(serial, inner)
	if _, err := c.conn.Write([]byte(frame)); err != nil {
		return "", newConnError("send: " + err.Error())
	}
	hdr := make([]byte, 26)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return "", newConnError("recv header: " + err.Error())
	}
	n, err := strconv.ParseUint(string(hdr[22:26]), 16, 16)
	if err != nil {
		return "", newConnError("invalid response length")
	}
	respBody := make([]byte, int(n))
	if _, err := io.ReadFull(c.conn, respBody); err != nil {
		return "", newConnError("recv body: " + err.Error())
	}
	return string(hdr) + string(respBody), nil
}

func chk4EBin(data []byte) ([]byte, error) {
	if len(data) < 15 {
		return nil, newConnError(fmt.Sprintf("short response (%d bytes)", len(data)))
	}
	if ec := binary.LittleEndian.Uint16(data[13:]); ec != 0 {
		return nil, &ProtocolError{EndCode: ec}
	}
	return data[15:], nil
}

func chk4EAsc(data string) (string, error) {
	if len(data) < 30 {
		return "", newConnError(fmt.Sprintf("short response (%d chars)", len(data)))
	}
	ec, err := strconv.ParseUint(data[26:30], 16, 16)
	if err != nil {
		return "", newConnError("invalid end code in response")
	}
	if ec != 0 {
		return "", &ProtocolError{EndCode: uint16(ec)}
	}
	return data[30:], nil
}

func (c *Client4E) validate(device string, start, count int) (string, error) {
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

func (c *Client4E) binBody(dev string, start, count int) []byte {
	b := make([]byte, 6)
	copy(b, addrBin(dev, start))
	binary.LittleEndian.PutUint16(b[4:], uint16(count))
	return b
}

// ReadWords reads count word values from device starting at address start.
func (c *Client4E) ReadWords(device string, start, count int) ([]uint16, error) {
	dev, err := c.validate(device, start, count)
	if err != nil {
		return nil, err
	}
	if c.mode == ModeBinary {
		resp, err := c.sendBin(cmdRead, subcWord, c.binBody(dev, start, count))
		if err != nil {
			return nil, err
		}
		raw, err := chk4EBin(resp)
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
	resp, err := c.sendAsc(cmdRead, subcWord, body)
	if err != nil {
		return nil, err
	}
	raw, err := chk4EAsc(resp)
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
func (c *Client4E) WriteWords(device string, start int, values []uint16) error {
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
		resp, err := c.sendBin(cmdWrite, subcWord, body)
		if err != nil {
			return err
		}
		_, err = chk4EBin(resp)
		return err
	}
	prefix := addrAsc(dev, start) + fmt.Sprintf("%04X", len(values))
	var sb strings.Builder
	sb.Grow(len(prefix) + len(values)*4)
	sb.WriteString(prefix)
	for _, v := range values {
		sb.WriteString(fmt.Sprintf("%04X", v))
	}
	resp, err := c.sendAsc(cmdWrite, subcWord, sb.String())
	if err != nil {
		return err
	}
	_, err = chk4EAsc(resp)
	return err
}

// ReadBits reads count bit values from device starting at address start.
func (c *Client4E) ReadBits(device string, start, count int) ([]bool, error) {
	dev, err := c.validate(device, start, count)
	if err != nil {
		return nil, err
	}
	if c.mode == ModeBinary {
		resp, err := c.sendBin(cmdRead, subcBit, c.binBody(dev, start, count))
		if err != nil {
			return nil, err
		}
		raw, err := chk4EBin(resp)
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
	resp, err := c.sendAsc(cmdRead, subcBit, body)
	if err != nil {
		return nil, err
	}
	raw, err := chk4EAsc(resp)
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
func (c *Client4E) WriteBits(device string, start int, values []bool) error {
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
		resp, err := c.sendBin(cmdWrite, subcBit, body)
		if err != nil {
			return err
		}
		_, err = chk4EBin(resp)
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
	resp, err := c.sendAsc(cmdWrite, subcBit, sb.String())
	if err != nil {
		return err
	}
	_, err = chk4EAsc(resp)
	return err
}

// RandomRead reads word and dword values from multiple devices (command 0x0403).
func (c *Client4E) RandomRead(words, dwords []DeviceAddr) ([]uint16, []uint32, error) {
	if len(words) > 255 || len(dwords) > 255 {
		return nil, nil, fmt.Errorf("device count must be <= 255")
	}
	wDevs, err := c.validateAddrs(words)
	if err != nil {
		return nil, nil, err
	}
	dDevs, err := c.validateAddrs(dwords)
	if err != nil {
		return nil, nil, err
	}
	if c.mode == ModeBinary {
		body := make([]byte, 0, 2+4*(len(words)+len(dwords)))
		body = append(body, byte(len(words)), byte(len(dwords)))
		for i, d := range words {
			body = append(body, addrBin(wDevs[i], d.Addr)...)
		}
		for i, d := range dwords {
			body = append(body, addrBin(dDevs[i], d.Addr)...)
		}
		resp, err := c.sendBin(0x0403, 0x0000, body)
		if err != nil {
			return nil, nil, err
		}
		raw, err := chk4EBin(resp)
		if err != nil {
			return nil, nil, err
		}
		expected := len(words)*2 + len(dwords)*4
		if len(raw) < expected {
			return nil, nil, newConnError(fmt.Sprintf("short payload: expected %d bytes, got %d", expected, len(raw)))
		}
		wVals := make([]uint16, len(words))
		for i := range wVals {
			wVals[i] = binary.LittleEndian.Uint16(raw[i*2:])
		}
		dVals := make([]uint32, len(dwords))
		off := len(words) * 2
		for i := range dVals {
			dVals[i] = binary.LittleEndian.Uint32(raw[off+i*4:])
		}
		return wVals, dVals, nil
	}
	body := fmt.Sprintf("%02X%02X", len(words), len(dwords))
	for i, d := range words {
		body += addrAsc(wDevs[i], d.Addr)
	}
	for i, d := range dwords {
		body += addrAsc(dDevs[i], d.Addr)
	}
	resp, err := c.sendAsc(0x0403, 0x0000, body)
	if err != nil {
		return nil, nil, err
	}
	raw, err := chk4EAsc(resp)
	if err != nil {
		return nil, nil, err
	}
	if expected := len(words)*4 + len(dwords)*8; len(raw) < expected {
		return nil, nil, newConnError(fmt.Sprintf("short payload: expected %d chars, got %d", expected, len(raw)))
	}
	wVals := make([]uint16, len(words))
	for i := range wVals {
		v, err := strconv.ParseUint(raw[i*4:(i+1)*4], 16, 16)
		if err != nil {
			return nil, nil, newConnError(fmt.Sprintf("invalid word at index %d: %v", i, err))
		}
		wVals[i] = uint16(v)
	}
	dVals := make([]uint32, len(dwords))
	off := len(words) * 4
	for i := range dVals {
		v, err := strconv.ParseUint(raw[off+i*8:off+(i+1)*8], 16, 32)
		if err != nil {
			return nil, nil, newConnError(fmt.Sprintf("invalid dword at index %d: %v", i, err))
		}
		dVals[i] = uint32(v)
	}
	return wVals, dVals, nil
}

// RandomWrite writes word and dword values to multiple devices (command 0x1402, subcmd 0x0000).
func (c *Client4E) RandomWrite(words []DeviceAddr, wordVals []uint16, dwords []DeviceAddr, dwordVals []uint32) error {
	if len(words) != len(wordVals) {
		return fmt.Errorf("words and wordVals must be same length")
	}
	if len(dwords) != len(dwordVals) {
		return fmt.Errorf("dwords and dwordVals must be same length")
	}
	if len(words) > 255 || len(dwords) > 255 {
		return fmt.Errorf("device count must be <= 255")
	}
	wDevs, err := c.validateAddrs(words)
	if err != nil {
		return err
	}
	dDevs, err := c.validateAddrs(dwords)
	if err != nil {
		return err
	}
	if c.mode == ModeBinary {
		body := make([]byte, 0, 2+6*len(words)+8*len(dwords))
		body = append(body, byte(len(words)), byte(len(dwords)))
		for i, d := range words {
			body = append(body, addrBin(wDevs[i], d.Addr)...)
			body = append(body, byte(wordVals[i]), byte(wordVals[i]>>8))
		}
		for i, d := range dwords {
			body = append(body, addrBin(dDevs[i], d.Addr)...)
			v := dwordVals[i]
			body = append(body, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		}
		resp, err := c.sendBin(0x1402, 0x0000, body)
		if err != nil {
			return err
		}
		_, err = chk4EBin(resp)
		return err
	}
	body := fmt.Sprintf("%02X%02X", len(words), len(dwords))
	for i, d := range words {
		body += addrAsc(wDevs[i], d.Addr) + fmt.Sprintf("%04X", wordVals[i])
	}
	for i, d := range dwords {
		body += addrAsc(dDevs[i], d.Addr) + fmt.Sprintf("%08X", dwordVals[i])
	}
	resp, err := c.sendAsc(0x1402, 0x0000, body)
	if err != nil {
		return err
	}
	_, err = chk4EAsc(resp)
	return err
}

// RandomWriteBits writes individual bit values to multiple devices (command 0x1402, subcmd 0x0001).
func (c *Client4E) RandomWriteBits(devices []DeviceAddr, values []bool) error {
	if len(devices) != len(values) {
		return fmt.Errorf("devices and values must be same length")
	}
	if len(devices) > 255 {
		return fmt.Errorf("device count must be <= 255")
	}
	devs, err := c.validateAddrs(devices)
	if err != nil {
		return err
	}
	if c.mode == ModeBinary {
		body := make([]byte, 0, 1+5*len(devices))
		body = append(body, byte(len(devices)))
		for i, d := range devices {
			body = append(body, addrBin(devs[i], d.Addr)...)
			if values[i] {
				body = append(body, 0x01)
			} else {
				body = append(body, 0x00)
			}
		}
		resp, err := c.sendBin(0x1402, 0x0001, body)
		if err != nil {
			return err
		}
		_, err = chk4EBin(resp)
		return err
	}
	body := fmt.Sprintf("%02X", len(devices))
	for i, d := range devices {
		body += addrAsc(devs[i], d.Addr)
		if values[i] {
			body += "01"
		} else {
			body += "00"
		}
	}
	resp, err := c.sendAsc(0x1402, 0x0001, body)
	if err != nil {
		return err
	}
	_, err = chk4EAsc(resp)
	return err
}

// validateAddrs validates a slice of DeviceAddr entries for Client4E.
func (c *Client4E) validateAddrs(devices []DeviceAddr) ([]string, error) {
	devs := make([]string, len(devices))
	for i, d := range devices {
		dev, err := c.validate(d.Device, d.Addr, 1)
		if err != nil {
			return nil, fmt.Errorf("device[%d]: %w", i, err)
		}
		devs[i] = dev
	}
	return devs, nil
}

// ---- Typed Read/Write Helpers (4E) ----

// ReadBool reads a single bit from a bit device.
func (c *Client4E) ReadBool(device string, start int) (bool, error) {
	bits, err := c.ReadBits(device, start, 1)
	if err != nil {
		return false, err
	}
	return bits[0], nil
}

// ReadInt16 reads a single 16-bit signed integer (1 word).
func (c *Client4E) ReadInt16(device string, start int) (int16, error) {
	words, err := c.ReadWords(device, start, 1)
	if err != nil {
		return 0, err
	}
	return int16(words[0]), nil
}

// ReadUInt16 reads a single 16-bit unsigned integer (1 word).
func (c *Client4E) ReadUInt16(device string, start int) (uint16, error) {
	words, err := c.ReadWords(device, start, 1)
	if err != nil {
		return 0, err
	}
	return words[0], nil
}

// ReadInt32 reads a single 32-bit signed integer (2 words).
func (c *Client4E) ReadInt32(device string, start int) (int32, error) {
	words, err := c.ReadWords(device, start, 2)
	if err != nil {
		return 0, err
	}
	return int32(uint32(words[0]) | uint32(words[1])<<16), nil
}

// ReadUInt32 reads a single 32-bit unsigned integer (2 words).
func (c *Client4E) ReadUInt32(device string, start int) (uint32, error) {
	words, err := c.ReadWords(device, start, 2)
	if err != nil {
		return 0, err
	}
	return uint32(words[0]) | uint32(words[1])<<16, nil
}

// ReadFloat32 reads a single 32-bit float (2 words).
func (c *Client4E) ReadFloat32(device string, start int) (float32, error) {
	v, err := c.ReadUInt32(device, start)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(v), nil
}

// ReadInt64 reads a single 64-bit signed integer (4 words).
func (c *Client4E) ReadInt64(device string, start int) (int64, error) {
	words, err := c.ReadWords(device, start, 4)
	if err != nil {
		return 0, err
	}
	v := uint64(words[0]) | uint64(words[1])<<16 | uint64(words[2])<<32 | uint64(words[3])<<48
	return int64(v), nil
}

// ReadUInt64 reads a single 64-bit unsigned integer (4 words).
func (c *Client4E) ReadUInt64(device string, start int) (uint64, error) {
	words, err := c.ReadWords(device, start, 4)
	if err != nil {
		return 0, err
	}
	return uint64(words[0]) | uint64(words[1])<<16 | uint64(words[2])<<32 | uint64(words[3])<<48, nil
}

// ReadFloat64 reads a single 64-bit float (4 words).
func (c *Client4E) ReadFloat64(device string, start int) (float64, error) {
	v, err := c.ReadUInt64(device, start)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}

// WriteValue writes a typed value to a device at start.
func (c *Client4E) WriteValue(device string, start int, value any) error {
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
