package common

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBodySpoolSealsIntoStorage covers the outbound spool used for rebuilt
// multipart bodies: in memory by default, on disk when the disk cache accepts
// the hinted size, and a discarded disk spool leaves no cache file behind.
// Deliberately not parallel: it changes the global disk-cache config.
func TestBodySpoolSealsIntoStorage(t *testing.T) {
	prev := GetDiskCacheConfig()
	defer SetDiskCacheConfig(prev)
	cacheDir := t.TempDir()
	payload := []byte("--boundary\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhello\r\n--boundary--\r\n")

	for _, tc := range []struct {
		name    string
		enabled bool
	}{
		{name: "memory", enabled: false},
		{name: "disk", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetDiskCacheConfig(DiskCacheConfig{Enabled: tc.enabled, ThresholdMB: 0, MaxSizeMB: 64, Path: cacheDir})

			spool := NewBodySpool(int64(len(payload)))
			defer spool.Discard()
			_, err := spool.Write(payload[:10])
			require.NoError(t, err)
			_, err = spool.Write(payload[10:])
			require.NoError(t, err)

			storage, err := spool.Finish()
			require.NoError(t, err)
			defer storage.Close()
			assert.Equal(t, tc.enabled, storage.IsDisk())
			assert.EqualValues(t, len(payload), storage.Size())
			got, err := io.ReadAll(storage)
			require.NoError(t, err)
			assert.Equal(t, payload, got)

			replay, err := storage.NewReader()
			require.NoError(t, err)
			got, err = io.ReadAll(replay)
			require.NoError(t, err)
			require.NoError(t, replay.Close())
			assert.Equal(t, payload, got)

			_, err = spool.Write(payload)
			assert.ErrorIs(t, err, ErrStorageClosed, "a finished spool takes no more writes")
			require.NoError(t, storage.Close())
			if tc.enabled {
				entries, err := os.ReadDir(GetDiskCacheDir())
				require.NoError(t, err)
				assert.Empty(t, entries, "closing the storage removes the spool file")
			}
		})
	}

	t.Run("discard removes an unfinished disk spool", func(t *testing.T) {
		SetDiskCacheConfig(DiskCacheConfig{Enabled: true, ThresholdMB: 0, MaxSizeMB: 64, Path: cacheDir})
		spool := NewBodySpool(int64(len(payload)))
		_, err := spool.Write(payload)
		require.NoError(t, err)
		spool.Discard()
		entries, err := os.ReadDir(GetDiskCacheDir())
		require.NoError(t, err)
		assert.Empty(t, entries)
		_, err = spool.Finish()
		assert.ErrorIs(t, err, ErrStorageClosed)
	})
}

// TestParseMultipartFormReusableKeepsDiskBackedUploadsOffHeap verifies that a
// request body the disk cache spooled to disk is parsed without copying it
// back to the heap: file parts land in multipart temp files while the form
// values and the replayable request body stay intact.
// Deliberately not parallel: it changes the global disk-cache config.
func TestParseMultipartFormReusableKeepsDiskBackedUploadsOffHeap(t *testing.T) {
	prev := GetDiskCacheConfig()
	defer SetDiskCacheConfig(prev)
	image := bytes.Repeat([]byte("x"), 64<<10)

	newContext := func(t *testing.T) *gin.Context {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("prompt", "a cat"))
		part, err := writer.CreateFormFile("image", "input.png")
		require.NoError(t, err)
		_, err = part.Write(image)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return c
	}

	for _, tc := range []struct {
		name       string
		enabled    bool
		fileOnDisk bool
	}{
		{name: "memory body keeps file parts in memory", enabled: false, fileOnDisk: false},
		{name: "disk body keeps file parts on disk", enabled: true, fileOnDisk: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetDiskCacheConfig(DiskCacheConfig{Enabled: tc.enabled, ThresholdMB: 0, MaxSizeMB: 64, Path: t.TempDir()})
			c := newContext(t)

			form, err := ParseMultipartFormReusable(c)
			require.NoError(t, err)
			t.Cleanup(func() { _ = form.RemoveAll() })
			storage, err := GetBodyStorage(c)
			require.NoError(t, err)
			assert.Equal(t, tc.enabled, storage.IsDisk())

			assert.Equal(t, "a cat", form.Value["prompt"][0])
			require.Len(t, form.File["image"], 1)
			file, err := form.File["image"][0].Open()
			require.NoError(t, err)
			defer file.Close()
			_, onDisk := file.(*os.File)
			assert.Equal(t, tc.fileOnDisk, onDisk)
			got, err := io.ReadAll(file)
			require.NoError(t, err)
			assert.Equal(t, image, got)

			// The request body is still replayable for pass-through and retries.
			replay, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			assert.EqualValues(t, len(replay), storage.Size())
			assert.Contains(t, string(replay), "a cat")
		})
	}
}
