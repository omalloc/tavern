//go:build linux
// +build linux

package rawdisk

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

type Device struct {
	file *os.File
	size uint64
}

// OpenDevice opens a block device or a sparse file using O_DIRECT | O_SYNC.
func OpenDevice(path string) (*Device, error) {
	flag := os.O_RDWR
	// Enable O_DIRECT and O_SYNC if available
	flag |= syscall.O_DIRECT | syscall.O_SYNC

	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return nil, err
	}

	// Get size
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}

	size := uint64(stat.Size())
	// Note: block devices might have 0 size in Stat().
	if size == 0 {
		// BLKGETSIZE64
		size, err = getBlockDeviceSize(f.Fd())
		if err != nil || size == 0 {
			f.Close()
			return nil, errors.New("cannot determine device size")
		}
	}

	return &Device{
		file: f,
		size: size,
	}, nil
}

func (d *Device) Close() error {
	return d.file.Close()
}

func (d *Device) Fd() uintptr {
	return d.file.Fd()
}

func (d *Device) Size() uint64 {
	return d.size
}

// getBlockDeviceSize attempts to determine the size of a block device via ioctl.
func getBlockDeviceSize(fd uintptr) (uint64, error) {
	// BLKGETSIZE64 is 0x80081272 on Linux
	var size uint64
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, 0x80081272, uintptr(unsafe.Pointer(&size)))
	if err != 0 {
		return 0, syscall.Errno(err)
	}
	return size, nil
}
