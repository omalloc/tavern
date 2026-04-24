package rawdisk

import (
	"syscall"
)

const DefaultAlignSize = 4096

// AlignedBlock allocates an aligned byte slice of the given size.
// It uses syscall.Mmap for anonymous, page-aligned memory.
// Size must be a multiple of AlignSize (typically 4096) for safe O_DIRECT usage.
func AlignedBlock(size int) ([]byte, error) {
	return syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
}

// FreeAlignedBlock frees an aligned byte slice.
func FreeAlignedBlock(b []byte) error {
	return syscall.Munmap(b)
}
