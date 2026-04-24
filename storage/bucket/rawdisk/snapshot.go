package rawdisk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var (
	ErrSnapshotCorrupted = errors.New("snapshot is corrupted")
	ErrSnapshotTooShort  = errors.New("snapshot data is too short")
)

// SnapshotHeader is placed at the beginning of the snapshot area.
type SnapshotHeader struct {
	Magic       uint32 // TAVR
	Version     uint32 // 1
	EntryCount  uint64 // Number of IndexEntry items
	WriteCursor uint64 // Cursor position when the snapshot was taken
}

// SerializeSnapshot writes the IndexTable to a byte slice.
// This data will be appended with a CRC32 checksum.
func SerializeSnapshot(table *IndexTable, cursor uint64) ([]byte, error) {
	buf := new(bytes.Buffer)

	header := SnapshotHeader{
		Magic:       MagicNumber,
		Version:     Version1,
		EntryCount:  uint64(table.Count()),
		WriteCursor: cursor,
	}

	// Write header
	err := binary.Write(buf, binary.LittleEndian, &header)
	if err != nil {
		return nil, err
	}

	// Write all valid entries
	table.Range(func(hash object.IDHash, entry *IndexEntry) bool {
		// Only persist non-expired entries
		if !entry.IsExpired() {
			buf.Write(hash[:]) // write array bytes
			binary.Write(buf, binary.LittleEndian, entry.Offset)
			binary.Write(buf, binary.LittleEndian, entry.Size)
			binary.Write(buf, binary.LittleEndian, entry.ExpiresAt)
		}
		return true
	})

	// Add CRC32 checksum
	crc := crc32.ChecksumIEEE(buf.Bytes())
	err = binary.Write(buf, binary.LittleEndian, crc)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// DeserializeSnapshot reads a snapshot byte slice and populates the given IndexTable.
// Returns the parsed SnapshotHeader for cursor initialization.
func DeserializeSnapshot(data []byte, table *IndexTable) (*SnapshotHeader, error) {
	// min size = header (24 bytes) + crc32 (4 bytes) = 28
	if len(data) < 28 {
		return nil, ErrSnapshotTooShort
	}

	buf := bytes.NewReader(data)
	header := &SnapshotHeader{}

	err := binary.Read(buf, binary.LittleEndian, &header.Magic)
	if err != nil { return nil, err }
	if header.Magic != MagicNumber {
		// Possibly uninitialized snapshot area, this is fine
		return nil, nil
	}

	binary.Read(buf, binary.LittleEndian, &header.Version)
	binary.Read(buf, binary.LittleEndian, &header.EntryCount)
	binary.Read(buf, binary.LittleEndian, &header.WriteCursor)

	// Since we padded the file and read a 16MB chunk, we need to locate the CRC
	// It's located right after the header + EntryCount * EntrySize
	// Wait, we didn't write it that way. In SerializeSnapshot, we wrote Header + Entries + CRC, and then when flushing to disk we padded it to 4096.
	// We need to calculate the exact size of serialized data BEFORE padding.
	entrySize := len(object.IDHash{}) + 8 + 4 + 8 // IDHash + Offset(8) + Size(4) + ExpiresAt(8)
	expectedLength := 24 + int(header.EntryCount) * entrySize

	if len(data) < expectedLength + 4 {
		return nil, ErrSnapshotTooShort
	}

	content := data[:expectedLength]
	expectedCRC := binary.LittleEndian.Uint32(data[expectedLength:expectedLength+4])
	actualCRC := crc32.ChecksumIEEE(content)

	if expectedCRC != actualCRC {
		return nil, ErrSnapshotCorrupted
	}

	for i := uint64(0); i < header.EntryCount; i++ {
		var hash object.IDHash
		// Read IDHash directly
		n, err := buf.Read(hash[:])
		if err == io.EOF || n < len(hash) {
			break
		}

		entry := &IndexEntry{}
		binary.Read(buf, binary.LittleEndian, &entry.Offset)
		binary.Read(buf, binary.LittleEndian, &entry.Size)
		binary.Read(buf, binary.LittleEndian, &entry.ExpiresAt)

		if !entry.IsExpired() {
			table.Set(hash, entry)
		}
	}

	return header, nil
}
