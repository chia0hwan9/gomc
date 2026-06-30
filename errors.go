package gomc

import "fmt"

// ProtocolError is returned when the PLC responds with a non-zero end code.
type ProtocolError struct {
	EndCode uint16
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("MC error 0x%04X", e.EndCode)
}

// ConnectionError is returned on network-level failures.
type ConnectionError struct {
	msg string
}

func (e *ConnectionError) Error() string {
	return e.msg
}

func newConnError(msg string) error {
	return &ConnectionError{msg: msg}
}
