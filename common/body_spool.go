package common

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

// BodySpool collects an outbound body while it is being produced and seals it
// into a BodyStorage. When the disk cache accepts a payload of the hinted
// size the bytes go straight to a cache file, so a rebuilt multi-megabyte
// upload never sits on the heap while the upstream call is in flight; otherwise
// they are collected in memory exactly like a bytes.Buffer.
type BodySpool struct {
	buf      bytes.Buffer
	file     *os.File
	filePath string
	sealed   bool
}

// NewBodySpool opens a spool for a body expected to be about sizeHint bytes.
func NewBodySpool(sizeHint int64) *BodySpool {
	if !ShouldUseDiskCache(sizeHint) {
		return &BodySpool{}
	}
	filePath, file, err := CreateDiskCacheFile(DiskCacheTypeBody)
	if err != nil {
		SysError(fmt.Sprintf("failed to create disk spool, falling back to memory: %v", err))
		return &BodySpool{}
	}
	return &BodySpool{file: file, filePath: filePath}
}

func (s *BodySpool) Write(p []byte) (int, error) {
	if s.sealed {
		return 0, ErrStorageClosed
	}
	if s.file != nil {
		return s.file.Write(p)
	}
	return s.buf.Write(p)
}

// Finish seals the spool and returns the storage that owns its contents. The
// caller closes that storage once the upstream call has finished.
func (s *BodySpool) Finish() (BodyStorage, error) {
	if s.sealed {
		return nil, ErrStorageClosed
	}
	s.sealed = true
	if s.file == nil {
		return newMemoryStorage(s.buf.Bytes()), nil
	}
	size, err := s.file.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = s.file.Seek(0, io.SeekStart)
	}
	if err != nil {
		s.file.Close()
		os.Remove(s.filePath)
		return nil, fmt.Errorf("failed to seek spool file: %w", err)
	}
	IncrementDiskFiles(size)
	return &diskStorage{file: s.file, filePath: s.filePath, size: size}, nil
}

// Discard releases a spool that was not finished, removing any cache file it
// created. It is a no-op after Finish, so callers may defer it unconditionally.
func (s *BodySpool) Discard() {
	if s.sealed {
		return
	}
	s.sealed = true
	if s.file != nil {
		s.file.Close()
		os.Remove(s.filePath)
	}
}
