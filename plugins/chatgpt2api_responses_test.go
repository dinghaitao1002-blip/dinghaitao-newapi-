package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadChatgpt2apiPlugin(t *testing.T) (*jsplugin.Registry, *jsplugin.LoadedPlugin) {
	t.Helper()
	source, err := builtinplugins.Source("chatgpt2api")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "chatgpt2api"})
	require.NoError(t, err)
	return registry, plugin
}

func chatgpt2apiJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}

func chatgpt2apiJSONList(t *testing.T, value any) []map[string]any {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded []map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}

func chatgpt2apiDecode(t *testing.T, plugin *jsplugin.LoadedPlugin, body map[string]any) (map[string]any, error) {
	t.Helper()
	value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
		"model": "gpt-image-2", "upstreamModel": "gpt-image-2", "stream": false,
		"body": map[string]any{"kind": "json", "value": body},
	})
	if err != nil {
		return nil, err
	}
	return chatgpt2apiJSON(t, value), nil
}

func TestChatgpt2apiDecodeReadsImageTool(t *testing.T) {
	_, plugin := loadChatgpt2apiPlugin(t)

	_, err := chatgpt2apiDecode(t, plugin, map[string]any{
		"model": "gpt-image-2", "input": "a red apple",
		"tools": []any{map[string]any{"type": "image_generation", "response_format": "jpeg"}},
	})
	require.ErrorContains(t, err, "response_format")

	decoded, err := chatgpt2apiDecode(t, plugin, map[string]any{
		"model": "gpt-image-2", "input": "a red apple", "instructions": "photo",
		"tools": []any{map[string]any{"type": "image_generation", "size": "1024x1536", "response_format": "b64_json"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "submit", decoded["kind"])
	assert.Equal(t, "gpt-image-2", decoded["model"])
	assert.Equal(t, "image", decoded["action"])
	requestBody := decoded["requestBody"].(map[string]any)
	assert.Equal(t, "b64_json", requestBody["response_format"])
	assert.Equal(t, "photo", requestBody["instructions"])
	assert.Equal(t, "a red apple", requestBody["input"])

	decoded, err = chatgpt2apiDecode(t, plugin, map[string]any{
		"model": "gpt-image-2", "input": "a red apple",
		"tools": []any{map[string]any{"type": "image_generation"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "url", decoded["requestBody"].(map[string]any)["response_format"])
}

func TestChatgpt2apiBuildSubmitForcesBackgroundAndForwardsSizeFit(t *testing.T) {
	_, plugin := loadChatgpt2apiPlugin(t)
	requestBody := map[string]any{
		"model": "gpt-image-2", "input": "a red apple", "response_format": "url",
		"tools": []any{map[string]any{"type": "image_generation", "size": "1024x1536"}},
	}
	value, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"baseUrl": "http://chatgpt2api-lb", "apiKey": "sk-channel", "action": "image",
		"model": "gpt-image-2", "upstreamModel": "gpt-image-2",
		"requestBody": requestBody, "requestHeaders": map[string]any{"Content-Type": "application/json", "X-Size-Fit": "crop"},
	})
	require.NoError(t, err)
	descriptor := chatgpt2apiJSON(t, value)
	assert.Equal(t, "http://chatgpt2api-lb/v1/responses", descriptor["url"])
	assert.Equal(t, "POST", descriptor["method"])
	headers := descriptor["headers"].(map[string]any)
	assert.Equal(t, "Bearer sk-channel", headers["Authorization"])
	assert.Equal(t, "crop", headers["X-Size-Fit"])
	body := descriptor["body"].(map[string]any)
	assert.Equal(t, true, body["background"])
	assert.Equal(t, "gpt-image-2", body["model"])
	_, hasStream := body["stream"]
	assert.False(t, hasStream)
	_, hasFormat := body["response_format"]
	assert.False(t, hasFormat, "response_format lives on the tool, not the body")
	tools := body["tools"].([]any)
	require.Len(t, tools, 1)
	assert.Equal(t, "1024x1536", tools[0].(map[string]any)["size"])

	value, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"baseUrl": "http://chatgpt2api-lb", "apiKey": "sk-channel", "action": "image",
		"model": "gpt-image-2", "upstreamModel": "gpt-image-2",
		"requestBody": requestBody, "requestHeaders": map[string]any{},
	})
	require.NoError(t, err)
	_, hasSizeFit := chatgpt2apiJSON(t, value)["headers"].(map[string]any)["X-Size-Fit"]
	assert.False(t, hasSizeFit)
}

func TestChatgpt2apiParseSubmitResponseRequiresResponseID(t *testing.T) {
	_, plugin := loadChatgpt2apiPlugin(t)
	value, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse",
		map[string]any{"requestBody": map[string]any{"response_format": "b64_json"}},
		map[string]any{"statusCode": 200, "body": map[string]any{"id": "resp_abc", "status": "queued"}})
	require.NoError(t, err)
	parsed := chatgpt2apiJSON(t, value)
	assert.Equal(t, "resp_abc", parsed["taskId"])
	assert.Equal(t, "queued", parsed["taskData"].(map[string]any)["status"])
	assert.Equal(t, map[string]any{"response_format": "b64_json"}, parsed["state"])

	_, err = plugin.Engine.Call(t.Context(), "parseSubmitResponse",
		map[string]any{"requestBody": map[string]any{}},
		map[string]any{"statusCode": 200, "body": map[string]any{"id": "task-1"}})
	require.ErrorContains(t, err, "resp_")

	// chatgpt2api's public submit errors (queue_full, invalid_response_format,
	// bad X-Size-Fit) must reach the client, not a generic "no resp_ id".
	_, err = plugin.Engine.Call(t.Context(), "parseSubmitResponse",
		map[string]any{"requestBody": map[string]any{}},
		map[string]any{"statusCode": 503, "body": map[string]any{"error": map[string]any{"code": "queue_full", "message": "the background queue is full, retry later"}}})
	require.ErrorContains(t, err, "queue_full")
	require.ErrorContains(t, err, "the background queue is full, retry later")

	_, err = plugin.Engine.Call(t.Context(), "parseSubmitResponse",
		map[string]any{"requestBody": map[string]any{}},
		map[string]any{"statusCode": 401, "body": "unauthorized"})
	require.ErrorContains(t, err, "HTTP 401")
}

func TestChatgpt2apiParseBatchResultMarksMissingAsFailure(t *testing.T) {
	_, plugin := loadChatgpt2apiPlugin(t)
	ctx := map[string]any{"baseUrl": "http://chatgpt2api-lb", "apiKey": "sk-channel"}
	tasks := []any{
		map[string]any{"taskId": "resp_done"}, map[string]any{"taskId": "resp_failed"},
		map[string]any{"taskId": "resp_running"}, map[string]any{"taskId": "resp_gone"},
	}

	value, err := plugin.Engine.Call(t.Context(), "buildBatchQueryRequest", ctx, tasks)
	require.NoError(t, err)
	descriptor := chatgpt2apiJSON(t, value)
	assert.Equal(t, "http://chatgpt2api-lb/v1/responses/batch", descriptor["url"])
	assert.Equal(t, "POST", descriptor["method"])
	assert.Equal(t, []any{"resp_done", "resp_failed", "resp_running", "resp_gone"}, descriptor["body"].(map[string]any)["ids"])
	assert.Equal(t, "Bearer sk-channel", descriptor["headers"].(map[string]any)["Authorization"])

	completed := map[string]any{"id": "resp_done", "status": "completed", "output": []any{
		map[string]any{"type": "image_generation_call", "status": "completed", "result": nil, "url": "https://img.example/a.png"},
	}}
	value, err = plugin.Engine.Call(t.Context(), "parseBatchResult", ctx, map[string]any{
		"data": []any{
			completed,
			map[string]any{"id": "resp_failed", "status": "failed", "error": map[string]any{"code": "no_available_account", "message": "no account available"}},
			map[string]any{"id": "resp_running", "status": "in_progress"},
			map[string]any{"id": "resp_odd", "status": "cancelled"},
		},
		"missing_ids": []any{"resp_gone"},
	}, map[string]any{"status": 200, "headers": map[string]any{}})
	require.NoError(t, err)
	items := chatgpt2apiJSONList(t, value)
	require.Len(t, items, 5)
	byID := map[string]map[string]any{}
	for _, item := range items {
		byID[item["taskId"].(string)] = item
	}
	assert.Equal(t, "SUCCESS", byID["resp_done"]["status"])
	assert.Nil(t, byID["resp_done"]["reason"])
	assert.Equal(t, "completed", byID["resp_done"]["data"].(map[string]any)["status"])
	assert.Equal(t, "FAILURE", byID["resp_failed"]["status"])
	assert.Equal(t, "no account available", byID["resp_failed"]["reason"])
	assert.Equal(t, "IN_PROGRESS", byID["resp_running"]["status"])
	assert.Nil(t, byID["resp_running"]["reason"])
	assert.Equal(t, "UNKNOWN", byID["resp_odd"]["status"])
	assert.Equal(t, "FAILURE", byID["resp_gone"]["status"])
	assert.Equal(t, "response not found or expired", byID["resp_gone"]["reason"])

	// An upstream error reply (400 too_many_ids, 401, ...) must surface as a
	// hook error so the poller counts it; an empty list would stall every task.
	_, err = plugin.Engine.Call(t.Context(), "parseBatchResult", ctx,
		map[string]any{"error": map[string]any{"code": "too_many_ids", "message": "at most 1000 ids per request"}},
		map[string]any{"status": 400, "headers": map[string]any{}})
	require.ErrorContains(t, err, "too_many_ids")
	_, err = plugin.Engine.Call(t.Context(), "parseBatchResult", ctx, "gateway timeout", map[string]any{"status": 504})
	require.ErrorContains(t, err, "HTTP 504")

	value, err = plugin.Engine.Call(t.Context(), "buildQueryRequest", map[string]any{"baseUrl": "http://chatgpt2api-lb", "apiKey": "sk-channel", "taskId": "resp_done"})
	require.NoError(t, err)
	assert.Equal(t, []any{"resp_done"}, chatgpt2apiJSON(t, value)["body"].(map[string]any)["ids"])

	value, err = plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{"taskId": "resp_done"}, completed, map[string]any{"status": 200})
	require.NoError(t, err)
	single := chatgpt2apiJSON(t, value)
	assert.Equal(t, "SUCCESS", single["status"])
	assert.Equal(t, "resp_done", single["data"].(map[string]any)["id"])
}

func TestChatgpt2apiUsageOnCompleteCountsCompletedItems(t *testing.T) {
	_, plugin := loadChatgpt2apiPlugin(t)
	value, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{"requestBody": map[string]any{"input": "x"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"image_count": float64(1)}, chatgpt2apiJSON(t, value))

	value, err = plugin.Engine.Call(t.Context(), "extractUsageOnComplete", map[string]any{"taskId": "resp_done"}, map[string]any{"status": "SUCCESS"}, map[string]any{
		"status": "completed", "output": []any{
			map[string]any{"type": "image_generation_call", "status": "completed", "url": "https://img.example/a.png"},
			map[string]any{"type": "message", "status": "completed"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"image_count": float64(1)}, chatgpt2apiJSON(t, value))

	value, err = plugin.Engine.Call(t.Context(), "extractUsageOnComplete", map[string]any{"taskId": "resp_failed"}, map[string]any{"status": "FAILURE"}, map[string]any{
		"status": "failed", "output": []any{}, "error": map[string]any{"code": "text_review"},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"image_count": float64(0)}, chatgpt2apiJSON(t, value))
}

func TestChatgpt2apiClaimsPlainModelsBackgroundOnly(t *testing.T) {
	registry, plugin := loadChatgpt2apiPlugin(t)
	assert.True(t, plugin.Meta.ProtocolSupports("openai_responses", "background"))
	assert.False(t, plugin.Meta.ProtocolSupports("openai_responses", "sync"))
	assert.False(t, plugin.Meta.ProtocolSupports("openai_responses", "stream"))
	require.Len(t, plugin.Meta.Protocols, 1)
	assert.Equal(t, "relay", plugin.Meta.Protocols[0].Unsupported, "sync and stream requests keep their relay channel")
	assert.ElementsMatch(t, []string{
		"gpt-image-2", "gpt-image-auto", "codex-gpt-image-2",
		"plus-codex-gpt-image-2", "team-codex-gpt-image-2", "pro-codex-gpt-image-2",
		"gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst",
		"gpt-image-2.6", "gpt-image-2.6-flare", "gpt-image-2.6-sunburst",
	}, plugin.Meta.Models)
	for _, model := range plugin.Meta.Models {
		binding, claimed := registry.Generation().LookupEndpoint("POST", "/v1/responses", model)
		require.True(t, claimed, model)
		assert.Same(t, plugin, binding.Plugin)
	}
	_, claimed := registry.Generation().LookupEndpoint("POST", "/v1/responses", "gpt-image-2-background")
	assert.False(t, claimed, "the alias is a chatgpt2api direct-call convenience, not a gateway model")
	assert.Equal(t, "batch", string(plugin.Meta.FetchMode))
	schema, ok := plugin.Meta.UsageSchema["image_count"]
	require.True(t, ok)
	assert.Equal(t, "Image generation unit price", schema.Description["en"])
	assert.Equal(t, "图片生成单价", schema.Description["zh"])
}

func TestChatgpt2apiRenderFinalPassesHint(t *testing.T) {
	_, plugin := loadChatgpt2apiPlugin(t)
	output := []any{map[string]any{"type": "image_generation_call", "status": "completed", "result": nil, "url": "https://img.example/a.png"}}
	value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderFinal"},
		map[string]any{"requestBody": map[string]any{"model": "gpt-image-2"}},
		map[string]any{"task_id": "task_public", "status": "SUCCESS", "data": map[string]any{
			"id": "resp_done", "status": "completed", "output": output, "metadata": map[string]any{"response_format": "b64_json"},
		}})
	require.NoError(t, err)
	payload := chatgpt2apiJSON(t, value)
	assert.Equal(t, "b64_json", payload["image_response_format"])
	assert.Equal(t, output, payload["output"])

	value, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderFinal"},
		map[string]any{"requestBody": map[string]any{"model": "gpt-image-2"}},
		map[string]any{"task_id": "task_public", "status": "SUCCESS", "data": map[string]any{"id": "resp_done", "status": "completed", "output": output}})
	require.NoError(t, err)
	assert.Equal(t, "url", chatgpt2apiJSON(t, value)["image_response_format"])
}

// A client that sends no image_generation tool puts size and response_format
// at the top level and reads the image from output[0].result.
func TestChatgpt2apiTopLevelFormWritesURLIntoResult(t *testing.T) {
	_, plugin := loadChatgpt2apiPlugin(t)
	input := []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_text", "text": "a red apple"},
		map[string]any{"type": "input_image", "image_url": "https://img.example/ref.jpg"},
	}}}

	decoded, err := chatgpt2apiDecode(t, plugin, map[string]any{
		"model": "gpt-image-2", "background": true, "size": "1024x1536", "response_format": "url", "input": input,
	})
	require.NoError(t, err)
	requestBody := decoded["requestBody"].(map[string]any)
	assert.Equal(t, []any{map[string]any{"type": "image_generation", "size": "1024x1536", "response_format": "url"}}, requestBody["tools"])
	assert.Equal(t, "url", requestBody["response_format"])
	assert.Equal(t, true, requestBody["url_in_result"])
	assert.Equal(t, input, requestBody["input"])

	decoded, err = chatgpt2apiDecode(t, plugin, map[string]any{"model": "gpt-image-2", "input": "a red apple"})
	require.NoError(t, err)
	requestBody = decoded["requestBody"].(map[string]any)
	assert.Equal(t, []any{map[string]any{"type": "image_generation"}}, requestBody["tools"])
	assert.Equal(t, true, requestBody["url_in_result"])

	// base64 already arrives in result, so the b64_json form needs no mark.
	decoded, err = chatgpt2apiDecode(t, plugin, map[string]any{"model": "gpt-image-2", "input": "a red apple", "response_format": "b64_json"})
	require.NoError(t, err)
	requestBody = decoded["requestBody"].(map[string]any)
	assert.Equal(t, "b64_json", requestBody["response_format"])
	assert.NotContains(t, requestBody, "url_in_result")

	_, err = chatgpt2apiDecode(t, plugin, map[string]any{"model": "gpt-image-2", "input": "a red apple", "response_format": "jpeg"})
	require.ErrorContains(t, err, "response_format")

	// A request that carries the tool keeps its form: top-level fields are not
	// read and result is left alone.
	decoded, err = chatgpt2apiDecode(t, plugin, map[string]any{
		"model": "gpt-image-2", "input": "a red apple", "size": "512x512", "response_format": "b64_json",
		"tools": []any{map[string]any{"type": "image_generation", "size": "1024x1024"}},
	})
	require.NoError(t, err)
	requestBody = decoded["requestBody"].(map[string]any)
	assert.Equal(t, []any{map[string]any{"type": "image_generation", "size": "1024x1024"}}, requestBody["tools"])
	assert.Equal(t, "url", requestBody["response_format"])
	assert.NotContains(t, requestBody, "url_in_result")

	value, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"baseUrl": "http://chatgpt2api-lb", "apiKey": "sk-channel", "action": "image", "model": "gpt-image-2", "upstreamModel": "gpt-image-2",
		"requestBody":    map[string]any{"model": "gpt-image-2", "input": "a red apple", "response_format": "url", "url_in_result": true, "tools": []any{map[string]any{"type": "image_generation"}}},
		"requestHeaders": map[string]any{},
	})
	require.NoError(t, err)
	assert.NotContains(t, chatgpt2apiJSON(t, value)["body"].(map[string]any), "url_in_result")

	value, err = plugin.Engine.Call(t.Context(), "parseSubmitResponse",
		map[string]any{"requestBody": map[string]any{"response_format": "url", "url_in_result": true}},
		map[string]any{"statusCode": 200, "body": map[string]any{"id": "resp_top", "status": "queued"}})
	require.NoError(t, err)
	state := chatgpt2apiJSON(t, value)["state"].(map[string]any)
	assert.Equal(t, map[string]any{"response_format": "url", "url_in_result": true}, state)

	completed := func(id string) map[string]any {
		return map[string]any{"id": id, "status": "completed", "metadata": map[string]any{"response_format": "url"}, "output": []any{
			map[string]any{"type": "image_generation_call", "status": "completed", "result": nil, "url": "https://img.example/" + id + ".png"},
		}}
	}
	value, err = plugin.Engine.Call(t.Context(), "parseBatchResult",
		map[string]any{"tasks": []any{
			map[string]any{"taskId": "resp_top", "state": state},
			map[string]any{"taskId": "resp_tool", "state": map[string]any{"response_format": "url"}},
		}},
		map[string]any{"data": []any{completed("resp_top"), completed("resp_tool")}, "missing_ids": []any{}},
		map[string]any{"status": 200, "headers": map[string]any{}})
	require.NoError(t, err)
	stored := map[string]map[string]any{}
	for _, item := range chatgpt2apiJSONList(t, value) {
		stored[item["taskId"].(string)] = item["data"].(map[string]any)
	}

	render := func(data map[string]any) map[string]any {
		rendered, renderErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderFinal"},
			map[string]any{"requestBody": map[string]any{}},
			map[string]any{"task_id": "task_public", "status": "SUCCESS", "data": data})
		require.NoError(t, renderErr)
		payload := chatgpt2apiJSON(t, rendered)
		assert.Equal(t, "url", payload["image_response_format"])
		return payload["output"].([]any)[0].(map[string]any)
	}
	topLevel := render(stored["resp_top"])
	assert.Equal(t, "https://img.example/resp_top.png", topLevel["result"])
	assert.Equal(t, "https://img.example/resp_top.png", topLevel["url"])
	withTool := render(stored["resp_tool"])
	assert.Nil(t, withTool["result"])
	assert.Equal(t, "https://img.example/resp_tool.png", withTool["url"])

	value, err = plugin.Engine.Call(t.Context(), "parseTaskResult",
		map[string]any{"taskId": "resp_top", "state": state}, completed("resp_top"), map[string]any{"status": 200})
	require.NoError(t, err)
	assert.Equal(t, "https://img.example/resp_top.png", render(chatgpt2apiJSON(t, value)["data"].(map[string]any))["result"])
}
