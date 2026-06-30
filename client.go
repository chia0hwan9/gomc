package gomc

// Client is the common interface implemented by all MC protocol clients
// (Client3E, Client4E, etc.).
type Client interface {
	ReadWords(device string, start, count int) ([]uint16, error)
	ReadBits(device string, start, count int) ([]bool, error)
	WriteWords(device string, start int, values []uint16) error
	WriteBits(device string, start int, values []bool) error
	ReadBool(device string, start int) (bool, error)
	ReadInt16(device string, start int) (int16, error)
	ReadUInt16(device string, start int) (uint16, error)
	ReadInt32(device string, start int) (int32, error)
	ReadUInt32(device string, start int) (uint32, error)
	ReadFloat32(device string, start int) (float32, error)
	WriteValue(device string, start int, value any) error
	RandomRead(words, dwords []DeviceAddr) ([]uint16, []uint32, error)
	RandomWrite(words []DeviceAddr, wordVals []uint16, dwords []DeviceAddr, dwordVals []uint32) error
	RandomWriteBits(devices []DeviceAddr, values []bool) error
	Connect() error
	Close() error
}
