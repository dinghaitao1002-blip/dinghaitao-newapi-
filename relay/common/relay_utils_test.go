package common

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeURLForLogMasksSensitiveQueryValues(t *testing.T) {
	rawURL := "https://example.test/v1beta/models/gemini:streamGenerateContent?alt=sse&key=sk-secret&access_token=ya29-secret&api-version=2024-02-01"

	got := SanitizeURLForLog(rawURL)

	assert.NotContains(t, got, "sk-secret")
	assert.NotContains(t, got, "ya29-secret")
	parsedURL, err := url.Parse(got)
	require.NoError(t, err)
	query := parsedURL.Query()
	assert.Equal(t, "***masked***", query.Get("key"))
	assert.Equal(t, "***masked***", query.Get("access_token"))
	assert.Equal(t, "sse", query.Get("alt"))
	assert.Equal(t, "2024-02-01", query.Get("api-version"))
}

func TestSanitizeURLForLogMasksAWSAndSecretLikeQueryKeys(t *testing.T) {
	rawURL := "https://example.test/path?X-Amz-Credential=credential&X-Amz-Signature=signature&session_token=session&client_secret=secret&model=gpt-test"

	got := SanitizeURLForLog(rawURL)

	assert.NotContains(t, got, "X-Amz-Credential=credential")
	assert.NotContains(t, got, "X-Amz-Signature=signature")
	assert.NotContains(t, got, "session_token=session")
	assert.NotContains(t, got, "client_secret=secret")
	parsedURL, err := url.Parse(got)
	require.NoError(t, err)
	query := parsedURL.Query()
	assert.Equal(t, "***masked***", query.Get("X-Amz-Credential"))
	assert.Equal(t, "***masked***", query.Get("X-Amz-Signature"))
	assert.Equal(t, "***masked***", query.Get("session_token"))
	assert.Equal(t, "***masked***", query.Get("client_secret"))
	assert.Equal(t, "gpt-test", query.Get("model"))
}

func TestSanitizeURLForLogKeepsURLWithoutSensitiveQuery(t *testing.T) {
	rawURL := "https://example.test/v1/chat/completions?api-version=2024-02-01&alt=sse"

	got := SanitizeURLForLog(rawURL)

	assert.Equal(t, rawURL, got)
}

func TestValidateMultipartDirectNormalizesImageField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.NewReader(`{"model":"wan2.7-i2v","prompt":"animate","image":" https://example.com/first.png "}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/video/generations", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	info := &RelayInfo{
		TaskRelayInfo: &TaskRelayInfo{},
	}

	taskErr := ValidateMultipartDirect(context, info)

	require.Nil(t, taskErr)
	storedReq, err := GetTaskRequest(context)
	require.NoError(t, err)
	require.Equal(t, []string{"https://example.com/first.png"}, storedReq.Images)
	require.Equal(t, constant.TaskActionImageToVideo, info.Action)
}

// TestTaskDurationBounds guards the billing invariant that user-supplied
// video duration (a quota multiplier via OtherRatio "seconds") is bounded, so
// it can never overflow quota calculation into a negative charge.
func TestTaskDurationBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func(t *testing.T, body string) (*gin.Context, *RelayInfo) {
		request := httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		return context, &RelayInfo{TaskRelayInfo: &TaskRelayInfo{}}
	}

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name:    "huge duration is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","duration":9999999999}`,
			wantErr: true,
		},
		{
			name:    "huge seconds string is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","seconds":"9999999999"}`,
			wantErr: true,
		},
		{
			name:    "negative duration is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","duration":-8}`,
			wantErr: true,
		},
		{
			name: "normal duration is accepted",
			body: `{"model":"sora-2","prompt":"a cat","seconds":"8"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" (multipart direct)", func(t *testing.T) {
			context, info := newContext(t, tt.body)
			taskErr := ValidateMultipartDirect(context, info)
			if tt.wantErr {
				require.NotNil(t, taskErr)
				require.Equal(t, "invalid_seconds", taskErr.Code)
			} else {
				require.Nil(t, taskErr)
			}
		})
		t.Run(tt.name+" (basic task request)", func(t *testing.T) {
			context, info := newContext(t, tt.body)
			taskErr := ValidateBasicTaskRequest(context, info, constant.TaskActionImageToVideo)
			if tt.wantErr {
				require.NotNil(t, taskErr)
				require.Equal(t, "invalid_seconds", taskErr.Code)
			} else {
				require.Nil(t, taskErr)
			}
		})
	}
}

// TestTaskRequestForwardsResolutionAndAspectRatio guards the /v1/videos
// contract for OpenAI-compatible video upstreams that take resolution and
// aspect_ratio as top-level fields: the stored task request must keep both so
// the upstream body carries them, and omit them when the client did not send
// them.
func TestTaskRequestForwardsResolutionAndAspectRatio(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name            string
		body            string
		wantResolution  string
		wantAspectRatio string
		wantInBody      []string
		wantNotInBody   []string
	}{
		{
			name:            "fields are kept and forwarded",
			body:            `{"model":"veo-3.1-fast","prompt":"a cat","duration":8,"resolution":"4k","aspect_ratio":"16:9"}`,
			wantResolution:  "4k",
			wantAspectRatio: "16:9",
			wantInBody:      []string{`"resolution":"4k"`, `"aspect_ratio":"16:9"`},
		},
		{
			name:          "absent fields stay absent",
			body:          `{"model":"veo-3.1-fast","prompt":"a cat","seconds":"8"}`,
			wantNotInBody: []string{`"resolution"`, `"aspect_ratio"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = request
			info := &RelayInfo{TaskRelayInfo: &TaskRelayInfo{}}

			taskErr := ValidateBasicTaskRequest(context, info, constant.TaskActionTextToVideo)
			require.Nil(t, taskErr)

			storedReq, err := GetTaskRequest(context)
			require.NoError(t, err)
			assert.Equal(t, tt.wantResolution, storedReq.Resolution)
			assert.Equal(t, tt.wantAspectRatio, storedReq.AspectRatio)

			encoded, err := common.Marshal(storedReq)
			require.NoError(t, err)
			for _, fragment := range tt.wantInBody {
				assert.Contains(t, string(encoded), fragment)
			}
			for _, fragment := range tt.wantNotInBody {
				assert.NotContains(t, string(encoded), fragment)
			}
		})
	}

	t.Run("multipart form keeps the fields top-level", func(t *testing.T) {
		var form bytes.Buffer
		writer := multipart.NewWriter(&form)
		for key, value := range map[string]string{
			"model": "veo-3.1-fast", "prompt": "a cat", "seconds": "8",
			"resolution": "1080p", "aspect_ratio": "9:16",
		} {
			require.NoError(t, writer.WriteField(key, value))
		}
		require.NoError(t, writer.Close())
		formBytes := form.Bytes()
		request := httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(formBytes))
		request.Header.Set("Content-Type", writer.FormDataContentType())
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		info := &RelayInfo{TaskRelayInfo: &TaskRelayInfo{}}
		// The relay middleware buffers the body and hands the handler a fresh reader.
		_, err := common.GetRequestBody(context)
		require.NoError(t, err)
		context.Request.Body = io.NopCloser(bytes.NewReader(formBytes))

		taskErr := ValidateBasicTaskRequest(context, info, constant.TaskActionTextToVideo)
		require.Nil(t, taskErr)

		storedReq, err := GetTaskRequest(context)
		require.NoError(t, err)
		assert.Equal(t, "1080p", storedReq.Resolution)
		assert.Equal(t, "9:16", storedReq.AspectRatio)
		assert.NotContains(t, storedReq.Metadata, "resolution")
		assert.NotContains(t, storedReq.Metadata, "aspect_ratio")
	})
}
