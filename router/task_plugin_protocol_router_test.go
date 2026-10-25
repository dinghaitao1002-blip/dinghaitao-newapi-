package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostProtocolRegistryDrivesProtocolRoutesOnce(t *testing.T) {
	engine := gin.New()
	SetTaskPluginProtocolRouter(engine)

	expected := []string{
		"POST /v1/responses",
		"GET /v1/responses/:response_id",
		"POST /v1/videos",
		"GET /v1/videos/:task_id",
		"GET /v1/videos/:task_id/content",
		"HEAD /v1/videos/:task_id/content",
		"POST /v1/images/generations",
		"POST /v1/images/edits",
	}
	actual := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		actual = append(actual, fmt.Sprintf("%s %s", route.Method, route.Path))
	}
	sort.Strings(expected)
	sort.Strings(actual)
	assert.Equal(t, expected, actual)
}

func TestImageProtocolFallbackPreservesGatewayAsyncOptIn(t *testing.T) {
	for _, operation := range []string{"generate", "edit"} {
		t.Run(operation, func(t *testing.T) {
			handlers, err := taskPluginProtocolHandlers("openai_image", operation)
			require.NoError(t, err)
			for _, optIn := range []string{"query", "prefer"} {
				t.Run(optIn, func(t *testing.T) {
					path := "/v1/images/generations"
					if operation == "edit" {
						path = "/v1/images/edits"
					}
					if optIn == "query" {
						path += "?async=true"
					}
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"ordinary-image","prompt":"circle"}`))
					c.Request.Header.Set("Content-Type", "application/json")
					if optIn == "prefer" {
						c.Request.Header.Set("Prefer", "respond-async")
					}
					// No endpoint is pinned for ordinary channels. With no worker running,
					// opt-in must reach its unavailable response, never synchronous relay.
					handlers[len(handlers)-1](c)
					assert.Equal(t, http.StatusServiceUnavailable, w.Code)
					assert.Contains(t, w.Body.String(), "image_async_unavailable")
				})
			}
		})
	}
}
