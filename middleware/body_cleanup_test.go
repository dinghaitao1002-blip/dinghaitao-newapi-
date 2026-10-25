package middleware

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A multipart form whose file part exceeds maxMemory is spooled to a
// multipart-* temp file. The form is attached to c.Request, which RequestId
// has replaced with a copy net/http does not own, so only BodyStorageCleanup
// can remove the temp file - including when the handler fails.
func TestBodyStorageCleanupRemovesParsedMultipartForm(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "ref.png")
	require.NoError(t, err)
	_, err = part.Write(bytes.Repeat([]byte("x"), 4096))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	router := gin.New()
	router.Use(RequestId())
	router.Use(BodyStorageCleanup())
	var spooled int
	router.POST("/v1/images/edits", func(c *gin.Context) {
		form, err := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary()).ReadForm(0)
		require.NoError(t, err)
		c.Request.MultipartForm = form
		spooled = countMultipartTempFiles(t, tmpDir)
		c.String(http.StatusBadGateway, "upstream failed")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, 1, spooled, "the file part must have been written to a multipart-* temp file")
	assert.Equal(t, 0, countMultipartTempFiles(t, tmpDir), "the temp file must be gone once the request ends")
}

func countMultipartTempFiles(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "multipart-*"))
	require.NoError(t, err)
	for _, name := range matches {
		_, err := os.Stat(name)
		require.NoError(t, err)
	}
	return len(matches)
}
