package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// The task store is deliberately separate from the short-lived relay body cache.
// Request/result files must survive client disconnects and process restarts.
func imageTaskDir(root, id string) string { return filepath.Join(root, id) }

func syncImageTaskDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func writeImageTaskRequest(dir string, body io.Reader, limit int64) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "request"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(body, limit+1))
	if copyErr == nil && n > limit {
		copyErr = common.ErrRequestBodyTooLarge
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := syncImageTaskDir(dir); err != nil {
		return err
	}
	return syncImageTaskDir(filepath.Dir(dir))
}

// Multipart boundaries differ between retries. Hash the ordered fields and file
// contents, not the wire boundary; repeated values retain their original order.
func imageTaskFingerprint(body io.Reader, contentType, operation, headers string) (string, error) {
	h := sha256.New()
	metadata, err := common.Marshal([]string{operation, headers})
	if err != nil {
		return "", err
	}
	h.Write(metadata)
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", err
	}
	if mediaType == "multipart/form-data" {
		type partDigest struct {
			Name, Filename, ContentType, Digest string
			Size                                int64
		}
		var parts []partDigest
		r := multipart.NewReader(body, params["boundary"])
		for {
			part, err := r.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			ph := sha256.New()
			n, err := io.Copy(ph, part)
			part.Close()
			if err != nil {
				return "", err
			}
			parts = append(parts, partDigest{part.FormName(), part.FileName(), part.Header.Get("Content-Type"), hex.EncodeToString(ph.Sum(nil)), n})
		}
		sort.SliceStable(parts, func(i, j int) bool { return parts[i].Name < parts[j].Name })
		data, err := common.Marshal(parts)
		if err != nil {
			return "", err
		}
		h.Write(data)
	} else {
		// Preserve number spelling and all provider-specific JSON fields.
		if _, err := io.Copy(h, body); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type imageTaskResponseWriter struct {
	header      http.Header
	status      int
	file        *os.File
	dir         string
	limit, size int64
	err         error
	committed   bool
}

func newImageTaskResponseWriter(dir string, limit int64) (*imageTaskResponseWriter, error) {
	f, err := os.OpenFile(filepath.Join(dir, "result.partial"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &imageTaskResponseWriter{header: make(http.Header), file: f, dir: dir, limit: limit}, nil
}
func (w *imageTaskResponseWriter) Header() http.Header { return w.header }
func (w *imageTaskResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *imageTaskResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.committed {
		return 0, errors.New("image task response already committed")
	}
	if int64(len(p)) > w.limit-w.size {
		w.err = errors.New("image task result exceeds storage limit")
		return 0, w.err
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}
func (w *imageTaskResponseWriter) Flush() {}
func (w *imageTaskResponseWriter) Commit() error {
	if w.err != nil {
		return w.err
	}
	if w.committed {
		return nil
	}
	if w.status < 200 || w.status >= 300 || w.size == 0 {
		return errors.New("image task has no successful result")
	}
	if strings.Contains(strings.ToLower(w.header.Get("Content-Type")), "text/event-stream") {
		return errors.New("streaming image results are not supported asynchronously")
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(w.dir, "result.partial"), filepath.Join(w.dir, "result.json")); err != nil {
		return err
	}
	if err := syncImageTaskDir(w.dir); err != nil {
		return err
	}
	w.committed = true
	return nil
}
func (w *imageTaskResponseWriter) Close() { _ = w.file.Close() }

func imageTaskStorageUsage(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed in image task storage")
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
