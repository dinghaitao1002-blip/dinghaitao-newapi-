package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlow2apiResponsesProtocol(t *testing.T) {
	testVideoResponsesProtocol(t, videoResponsesTestCase{
		pluginKey: "flow2api",
		model:     "veo-3.1-fast",
		requestBody: map[string]any{
			"model":        "veo-3.1-fast",
			"input":        "一只小猫追蝴蝶",
			"seconds":      8,
			"resolution":   "1080p",
			"aspect_ratio": "16:9",
		},
		wantAction: "text_to_video",
		wantRequest: map[string]any{
			"model":        "veo-3.1-fast",
			"prompt":       "一只小猫追蝴蝶",
			"seconds":      float64(8),
			"resolution":   "1080p",
			"aspect_ratio": "16:9",
		},
		wantUsageKeys:  []string{"seconds", "resolution"},
		wantVendorName: "flow2api",
	})
}

func loadFlow2apiPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("flow2api")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "flow2api"})
	require.NoError(t, err)
	return plugin
}

func flow2apiJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}

func TestFlow2apiDeclaresFlowModelsWithoutLegacyChannelTypes(t *testing.T) {
	plugin := loadFlow2apiPlugin(t)
	assert.Equal(t, []string{"veo-3.1-lite", "veo-3.1-fast", "veo-3.1-quality", "omni-1.1-flash"}, plugin.Meta.Models)
	// The OpenAI channel type belongs to sora; flow2api lives on Task Plugin channels.
	assert.Empty(t, plugin.Meta.ChannelTypes)
}

// The /v1/videos body is forwarded verbatim, so flow2api's resolution and
// aspect_ratio parameters survive the gateway (the generic task path drops them).
func TestFlow2apiVideoDecodeKeepsFlowParameters(t *testing.T) {
	plugin := loadFlow2apiPlugin(t)
	value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
		"model": "veo-3.1-quality", "upstreamModel": "veo-3.1-quality", "stream": false,
		"body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "veo-3.1-quality", "prompt": "一只纸船", "duration": 8, "resolution": "1080p", "aspect_ratio": "9:16",
		}},
	})
	require.NoError(t, err)
	decoded := flow2apiJSON(t, value)
	assert.Equal(t, "submit", decoded["kind"])
	assert.Equal(t, "text_to_video", decoded["action"])
	body := decoded["requestBody"].(map[string]any)
	assert.Equal(t, float64(8), body["duration"])
	assert.Equal(t, "1080p", body["resolution"])
	assert.Equal(t, "9:16", body["aspect_ratio"])

	_, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
		"model": "veo-3.1-quality", "stream": false,
		"body": map[string]any{"kind": "json", "value": map[string]any{"model": "veo-3.1-quality", "prompt": "x", "seconds": 9999}},
	})
	require.ErrorContains(t, err, "seconds")
}

func TestFlow2apiBuildSubmitPostsToFlowVideos(t *testing.T) {
	plugin := loadFlow2apiPlugin(t)
	value, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"action": "text_to_video", "model": "veo-3.1-fast", "upstreamModel": "veo-3.1-fast",
		"baseUrl": "http://flow2api:8000", "apiKey": "flow-key",
		"requestBody": map[string]any{"model": "veo-3.1-fast", "prompt": "a cat", "duration": 8, "resolution": "1080p", "aspect_ratio": "16:9"},
	})
	require.NoError(t, err)
	descriptor := flow2apiJSON(t, value)
	assert.Equal(t, "http://flow2api:8000/v1/videos", descriptor["url"])
	assert.Equal(t, "POST", descriptor["method"])
	headers := descriptor["headers"].(map[string]any)
	assert.Equal(t, "Bearer flow-key", headers["Authorization"])
	body := descriptor["body"].(map[string]any)
	assert.Equal(t, "veo-3.1-fast", body["model"])
	assert.Equal(t, "1080p", body["resolution"])
	assert.Equal(t, "16:9", body["aspect_ratio"])
}

// Billing facts: seconds defaults to flow2api's 8-second default, duration is
// accepted as an alias, and resolution is normalized onto the declared enum.
func TestFlow2apiUsageFacts(t *testing.T) {
	plugin := loadFlow2apiPlugin(t)
	cases := []struct {
		name           string
		body           map[string]any
		wantSeconds    float64
		wantResolution string
	}{
		{"defaults", map[string]any{"model": "veo-3.1-fast", "prompt": "x"}, 8, "720p"},
		{"seconds string", map[string]any{"model": "veo-3.1-lite", "prompt": "x", "seconds": "8", "resolution": "1080p"}, 8, "1080p"},
		{"duration alias", map[string]any{"model": "omni-1.1-flash", "prompt": "x", "duration": 10}, 10, "720p"},
		{"sora size", map[string]any{"model": "veo-3.1-fast", "prompt": "x", "size": "1920x1080"}, 8, "1080p"},
		{"unknown resolution falls back", map[string]any{"model": "veo-3.1-fast", "prompt": "x", "resolution": "4k"}, 8, "720p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
				"action": "text_to_video", "model": tc.body["model"], "upstreamModel": tc.body["model"], "requestBody": tc.body,
			})
			require.NoError(t, err)
			facts := flow2apiJSON(t, value)
			assert.Equal(t, tc.wantSeconds, facts["seconds"])
			assert.Equal(t, tc.wantResolution, facts["resolution"])
		})
	}
}

func TestFlow2apiParseTaskResultMapsStatusesAndKeepsURL(t *testing.T) {
	plugin := loadFlow2apiPlugin(t)
	call := func(body map[string]any) map[string]any {
		value, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{"taskId": "video_1"}, body, map[string]any{"status": 200})
		require.NoError(t, err)
		return flow2apiJSON(t, value)
	}
	running := call(map[string]any{"id": "video_1", "status": "in_progress", "progress": 42})
	assert.Equal(t, "IN_PROGRESS", running["status"])
	assert.Equal(t, "42%", running["progress"])

	done := call(map[string]any{"id": "video_1", "status": "completed", "progress": 100, "url": "https://flow-content.google/video/abc?sig=1"})
	assert.Equal(t, "SUCCESS", done["status"])
	assert.Equal(t, "https://flow-content.google/video/abc?sig=1", done["url"])

	failed := call(map[string]any{"id": "video_1", "status": "failed", "error": map[string]any{"message": "生成失败: USER_MINOR"}})
	assert.Equal(t, "FAILURE", failed["status"])
	assert.Equal(t, "生成失败: USER_MINOR", failed["reason"])

	unknown := call(map[string]any{"id": "video_1", "status": "weird"})
	assert.Equal(t, "UNKNOWN", unknown["status"])
}

// flow2api serves no id-based content route; the artifact is the public CDN
// link in task.data, fetched without credentials.
func TestFlow2apiContentRequestUsesResultURL(t *testing.T) {
	plugin := loadFlow2apiPlugin(t)
	value, err := plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{
		"artifactKey": "video", "upstreamTaskId": "video_1", "baseUrl": "http://flow2api:8000", "apiKey": "flow-key",
		"data":          map[string]any{"url": "https://flow-content.google/video/abc?sig=1"},
		"clientRequest": map[string]any{"method": "GET", "headers": map[string]any{}},
	})
	require.NoError(t, err)
	descriptor := flow2apiJSON(t, value)
	assert.Equal(t, "https://flow-content.google/video/abc?sig=1", descriptor["url"])
	assert.Equal(t, true, descriptor["credentialless"])
	assert.Nil(t, descriptor["headers"])

	artifacts, err := plugin.Engine.Call(t.Context(), "listArtifacts", map[string]any{"taskId": "video_1", "status": "SUCCESS", "action": "text_to_video", "data": map[string]any{"url": "https://x"}})
	require.NoError(t, err)
	encoded, err := common.Marshal(artifacts)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"key":"video","type":"video"}]`, string(encoded))
}
