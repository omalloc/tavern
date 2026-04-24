package rawdisk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

const (
	MagicNumber    = 0x54415652 // 'TAVR'
	Version1       = 1
	SuperblockSize = 4096 // 4KB
)

var (
	ErrInvalidMagic       = errors.New("invalid magic number")
	ErrInvalidCRC         = errors.New("invalid crc32")
	ErrUnsupportedVersion = errors.New("unsupported version")
	ErrDataTooShort       = errors.New("data too short for superblock")
)

// Superblock records basic information about the raw disk and snapshot area
type Superblock struct {
	Magic   uint32 // Magic number 'TAVR'
	Version uint32 // Version number, e.g., 1

	BlockSize   uint32 // Logical block size, e.g., 4096
	TotalBlocks uint64 // Total number of blocks on the disk

	SnapshotOffset uint64 // Byte offset of the snapshot area
	SnapshotSize   uint64 // Byte size of the snapshot area
	SnapshotActive uint8  // Currently active snapshot area (0: A, 1: B)

	StripeOffset uint64 // Byte offset of the stripe data area
	StripeSize   uint64 // Byte size of the stripe data area

	WriteCursor uint64 // Global write cursor for the ring buffer
}

// Marshal serializes the Superblock to a 4KB aligned byte slice and appends a CRC32 checksum.
func (s *Superblock) Marshal() ([]byte, error) {
	buf := new(bytes.Buffer)

	// Write fields
	binary.Write(buf, binary.LittleEndian, s.Magic)
	binary.Write(buf, binary.LittleEndian, s.Version)
	binary.Write(buf, binary.LittleEndian, s.BlockSize)
	binary.Write(buf, binary.LittleEndian, s.TotalBlocks)
	binary.Write(buf, binary.LittleEndian, s.SnapshotOffset)
	binary.Write(buf, binary.LittleEndian, s.SnapshotSize)
	binary.Write(buf, binary.LittleEndian, s.SnapshotActive)
	binary.Write(buf, binary.LittleEndian, s.StripeOffset)
	binary.Write(buf, binary.LittleEndian, s.StripeSize)
	binary.Write(buf, binary.LittleEndian, s.WriteCursor)

	// Calculate padding to make it 4096 bytes including the CRC (4 bytes at the end)
	currentLen := buf.Len()
	paddingLen := SuperblockSize - currentLen - 4
	if paddingLen > 0 {
		padding := make([]byte, paddingLen)
		buf.Write(padding)
	}

	// Calculate CRC32 and append
	crc := crc32.ChecksumIEEE(buf.Bytes())
	binary.Write(buf, binary.LittleEndian, crc)

	return buf.Bytes(), nil
}

// UnmarshalSuperblock parses a 4KB aligned byte slice into a Superblock, verifying the CRC32 checksum.
func UnmarshalSuperblock(data []byte) (*Superblock, error) {
	if len(data) < SuperblockSize {
		return nil, ErrDataTooShort
	}

	contentSize := SuperblockSize - 4
	content := data[:contentSize]
	expectedCRC := binary.LittleEndian.Uint32(data[contentSize:SuperblockSize])
	actualCRC := crc32.ChecksumIEEE(content)

	if expectedCRC != actualCRC {
		return nil, ErrInvalidCRC
	}

	s := &Superblock{}
	buf := bytes.NewReader(content)

	binary.Read(buf, binary.LittleEndian, &s.Magic)
	if s.Magic != MagicNumber {
		return nil, ErrInvalidMagic
	}

	binary.Read(buf, binary.LittleEndian, &s.Version)
	if s.Version != Version1 {
		return nil, ErrUnsupportedVersion
	}

	binary.Read(buf, binary.LittleEndian, &s.BlockSize)
	binary.Read(buf, binary.LittleEndian, &s.TotalBlocks)
	binary.Read(buf, binary.LittleEndian, &s.SnapshotOffset)
	binary.Read(buf, binary.LittleEndian, &s.SnapshotSize)
	binary.Read(buf, binary.LittleEndian, &s.SnapshotActive)
	binary.Read(buf, binary.LittleEndian, &s.StripeOffset)
	binary.Read(buf, binary.LittleEndian, &s.StripeSize)
	binary.Read(buf, binary.LittleEndian, &s.WriteCursor)

	return s, nil
}
