# gomc

[![Go Reference](https://pkg.go.dev/badge/github.com/chia0hwan9/gomc.svg)](https://pkg.go.dev/github.com/chia0hwan9/gomc)

A Go library for communicating with Mitsubishi PLCs over Ethernet using the MC Protocol (SLMP).

## Features

- **3E frame** — TCP and UDP transport, Binary and ASCII modes
- **4E frame** — TCP transport, Binary and ASCII modes, with serial number tracking
- **Batch read/write** — words and bits from contiguous address ranges
- **Typed helpers** — `bool`, `int16`, `uint16`, `int32`, `uint32`, `float32`, `int64`, `uint64`, `float64`, and `string`
- **Random access** — read/write multiple non-contiguous devices in a single request
- **Remote control** — Run, Stop, Pause, Latch Clear, and Reset
- **Goroutine-safe** — concurrent callers are serialized with an internal mutex
- **Common `Client` interface** — write transport-agnostic code that works with any frame type

## Installation

```
go get github.com/chia0hwan9/gomc
```

Requires Go 1.25+.

## Quick Start

### 3E Frame (TCP)

```go
package main

import (
    "fmt"
    mc "github.com/chia0hwan9/gomc"
)

func main() {
    c, err := mc.New3EClient("192.168.0.1", 5007, mc.ModeBinary)
    if err != nil {
        panic(err)
    }
    if err := c.Connect(); err != nil {
        panic(err)
    }
    defer c.Close()

    // Read 5 words from D100
    words, err := c.ReadWords("D", 100, 5)
    if err != nil {
        panic(err)
    }
    fmt.Println(words)

    // Write to D200
    if err := c.WriteWords("D", 200, []uint16{1, 2, 3}); err != nil {
        panic(err)
    }
}
```

### 3E Frame (UDP)

```go
c, err := mc.New3EClientUDP("192.168.0.1", 5007, mc.ModeBinary)
```

### 4E Frame

```go
c, err := mc.New4EClient("192.168.0.1", 5007, mc.ModeBinary)
```

### Generic Client Interface

Use the `Client` interface to write code that works with any frame type:

```go
func readTemperature(c mc.Client) (int16, error) {
    return c.ReadInt16("D", 100)
}

// Works with any client type
c3e, _ := mc.New3EClient("192.168.0.1", 5007, mc.ModeBinary)
c3e.Connect()
readTemperature(c3e)

c4e, _ := mc.New4EClient("192.168.0.1", 5007, mc.ModeBinary)
c4e.Connect()
readTemperature(c4e)
```

## Client Interface

Both `Client3E` and `Client4E` implement the `Client` interface:

### Batch Read/Write

| Method | Description |
|--------|-------------|
| `ReadWords(device string, start, count int) ([]uint16, error)` | Read word values from a contiguous range |
| `WriteWords(device string, start int, values []uint16) error` | Write word values to a contiguous range |
| `ReadBits(device string, start, count int) ([]bool, error)` | Read bit values from a contiguous range |
| `WriteBits(device string, start int, values []bool) error` | Write bit values to a contiguous range |

### Typed Read/Write Helpers

| Method | Description |
|--------|-------------|
| `ReadBool(device string, start int) (bool, error)` | Read a single bit |
| `ReadInt16(device string, start int) (int16, error)` | Read a 16-bit signed integer (1 word) |
| `ReadUInt16(device string, start int) (uint16, error)` | Read a 16-bit unsigned integer (1 word) |
| `ReadInt32(device string, start int) (int32, error)` | Read a 32-bit signed integer (2 words, LE) |
| `ReadUInt32(device string, start int) (uint32, error)` | Read a 32-bit unsigned integer (2 words, LE) |
| `ReadFloat32(device string, start int) (float32, error)` | Read a 32-bit float (2 words, LE) |
| `WriteValue(device string, start int, value any) error` | Write a typed value (`bool`, `int8`–`int64`, `uint8`–`uint64`, `float32`, `float64`, `string`) |

### Random Access

| Method | Description |
|--------|-------------|
| `RandomRead(words, dwords []DeviceAddr) ([]uint16, []uint32, error)` | Read multiple non-contiguous word/dword devices in one request |
| `RandomWrite(words []DeviceAddr, wordVals []uint16, dwords []DeviceAddr, dwordVals []uint32) error` | Write multiple non-contiguous word/dword devices in one request |
| `RandomWriteBits(devices []DeviceAddr, values []bool) error` | Write individual bits to multiple non-contiguous devices in one request |

### Connection Lifecycle

| Method | Description |
|--------|-------------|
| `Connect() error` | Establish the TCP or UDP connection |
| `Close() error` | Close the connection |

### Client-Specific Methods

These methods exist on `*Client3E` and `*Client4E` but are not part of the `Client` interface:

| Method | Description |
|--------|-------------|
| `SetTimeout(d time.Duration)` | Set per-request I/O deadline (default: 5s; ≤0 disables) |

#### Extended Typed Helpers

| Method | Description |
|--------|-------------|
| `ReadInt64(device string, start int) (int64, error)` | Read a 64-bit signed integer (4 words, LE) |
| `ReadUInt64(device string, start int) (uint64, error)` | Read a 64-bit unsigned integer (4 words, LE) |
| `ReadFloat64(device string, start int) (float64, error)` | Read a 64-bit float (4 words, LE) |

#### Remote Control

| Method | Description |
|--------|-------------|
| `RemoteRun(clearMode int, force bool) error` | Start the PLC CPU (clearMode: 0=none, 1=except latch, 2=all) |
| `RemoteStop() error` | Stop the PLC CPU |
| `RemotePause(force bool) error` | Pause the PLC CPU |
| `RemoteLatchClear() error` | Clear latch (PLC must be stopped) |
| `RemoteReset() error` | Reset the PLC (connection will close; expect a `ConnectionError`) |

## Framing Modes

| Constant | Description |
|----------|-------------|
| `ModeBinary` | Binary framing (compact, recommended) |
| `ModeASCII` | ASCII framing (human-readable for debugging) |

## Supported Devices

### Word Devices

| Device | Description |
|--------|-------------|
| `D` | Data register |
| `W` | Link register |
| `R` | File register |
| `ZR` | File register (extended) |
| `SW` | Link special register |
| `SD` | Special register |
| `TN` | Timer current value |
| `STN` | Retentive timer current value |
| `CN` | Counter current value |
| `Z` | Index register |

### Bit Devices

| Device | Description |
|--------|-------------|
| `X` | Input |
| `Y` | Output |
| `M` | Internal relay |
| `L` | Latch relay |
| `V` | Edge relay |
| `S` | Step relay |
| `DX` | Direct access input |
| `DY` | Direct access output |
| `TC` | Timer coil |
| `TS` | Timer contact |
| `STC` | Retentive timer coil |
| `STS` | Retentive timer contact |
| `CC` | Counter coil |
| `CS` | Counter contact |
| `B` | Link relay |
| `F` | Annunciator |
| `SB` | Link special relay |
| `SM` | Special relay |

Device names are case-insensitive.

## Random Access

Read and write non-contiguous devices in a single round-trip (up to 255 word devices and 255 dword devices per request):

```go
// Read D100 (word), D200 (word), and D300 (dword) in one request
words, dwords, err := c.RandomRead(
    []mc.DeviceAddr{{Device: "D", Addr: 100}, {Device: "D", Addr: 200}},
    []mc.DeviceAddr{{Device: "D", Addr: 300}},
)

// Write D100=10, D200=20 (words) and D300=100000 (dword)
err = c.RandomWrite(
    []mc.DeviceAddr{{Device: "D", Addr: 100}, {Device: "D", Addr: 200}},
    []uint16{10, 20},
    []mc.DeviceAddr{{Device: "D", Addr: 300}},
    []uint32{100000},
)

// Write bits to M0, M10, Y5
err = c.RandomWriteBits(
    []mc.DeviceAddr{{Device: "M", Addr: 0}, {Device: "M", Addr: 10}, {Device: "Y", Addr: 5}},
    []bool{true, false, true},
)
```

## Error Handling

```go
words, err := c.ReadWords("D", 100, 5)
if err != nil {
    var mcErr *mc.ProtocolError
    if errors.As(err, &mcErr) {
        // PLC returned a non-zero end code
        fmt.Printf("PLC error: 0x%04X\n", mcErr.EndCode)
    } else {
        // ConnectionError — network or I/O failure
        fmt.Println("connection error:", err)
    }
}
```

| Error Type | Description |
|------------|-------------|
| `*ProtocolError` | PLC responded with a non-zero end code; inspect `.EndCode` |
| `*ConnectionError` | Network-level failure (connect, send, recv, timeout) |

## Address Parsing

```go
device, start, err := mc.ParseAddress("D100")
// device = "D", start = 100

device, start, err = mc.ParseAddress("M50")
// device = "M", start = 50
```

## Remote Control Example

```go
// Graceful restart sequence
c.RemoteStop()                    // Stop the PLC
c.RemoteLatchClear()              // Clear latch data
c.RemoteRun(0, false)             // Run without clearing memory
```

## PLC Setup

The PLC must have Ethernet communication enabled with SLMP (MC Protocol) configured.
Default port is typically `5007` for Q / iQ-R series, though some configurations use other ports.

## Concurrency

Requests on the same client instance are safe for concurrent use. Each call acquires an internal mutex, so only one request/response exchange is active on the connection at a time.
