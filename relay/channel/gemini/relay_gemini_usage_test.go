package gemini

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamResponseGeminiChat2OpenAIAttachesUsageMetadata(t *testing.T) {
	t.Parallel()

	withUsage, isStop := streamResponseGeminiChat2OpenAI(&dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "hello"}},
			},
		}},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     3868,
			CandidatesTokenCount: 0,
			TotalTokenCount:      3868,
		},
	})
	require.False(t, isStop)
	require.NotNil(t, withUsage)
	require.NotNil(t, withUsage.Usage)
	require.Equal(t, 3868, withUsage.Usage.PromptTokens)
	require.Equal(t, 3868, withUsage.Usage.TotalTokens)
	require.NotNil(t, withUsage.Usage.BillingUsage)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, withUsage.Usage.BillingUsage.Source)
	require.Equal(t, dto.BillingUsageSemanticGemini, withUsage.Usage.BillingUsage.Semantic)
	require.NotNil(t, withUsage.Usage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, 3868, withUsage.Usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	require.False(t, withUsage.Usage.BillingUsage.Estimated)

	withoutUsage, _ := streamResponseGeminiChat2OpenAI(&dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "hello"}},
			},
		}},
	})
	require.NotNil(t, withoutUsage)
	require.Nil(t, withoutUsage.Usage)
}

func TestGeminiChatStreamHandlerClaudeFirstFrameUsesUpstreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatClaude,
		OriginModelName: "gemini-2.5-flash",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-2.5-flash",
		},
		ClaudeConvertInfo: &relaycommon.ClaudeConvertInfo{
			LastMessagesType: relaycommon.LastMessageTypeNone,
		},
	}
	info.SetEstimatePromptTokens(4994)

	chunkData, err := common.Marshal(dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: []dto.GeminiPart{{Text: "hello"}},
			},
		}},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount: 3868,
			TotalTokenCount:  3868,
		},
	})
	require.NoError(t, err)
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte("data: " + string(chunkData) + "\n" + "data: [DONE]\n"))),
	}

	usage, newAPIError := GeminiChatStreamHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 3868, usage.PromptTokens)

	var startUsage, deltaUsage *dto.ClaudeUsage
	for line := range strings.SplitSeq(recorder.Body.String(), "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok {
			continue
		}
		var event dto.ClaudeResponse
		if err := common.UnmarshalJsonStr(payload, &event); err != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				startUsage = event.Message.Usage
			}
		case "message_delta":
			deltaUsage = event.Usage
		}
	}

	require.NotNil(t, startUsage)
	require.Equal(t, 3868, startUsage.InputTokens)
	require.NotNil(t, startUsage.BillingUsage)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, startUsage.BillingUsage.Source)
	require.Equal(t, dto.BillingUsageSemanticGemini, startUsage.BillingUsage.Semantic)
	require.NotNil(t, startUsage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, 3868, startUsage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	require.False(t, startUsage.BillingUsage.Estimated)

	require.NotNil(t, deltaUsage)
	require.Equal(t, 3868, deltaUsage.InputTokens)
	require.NotNil(t, deltaUsage.BillingUsage)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, deltaUsage.BillingUsage.Source)
	require.Equal(t, dto.BillingUsageSemanticGemini, deltaUsage.BillingUsage.Semantic)
	require.NotNil(t, deltaUsage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, 3868, deltaUsage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
}

func TestGeminiChatHandlerCompletionTokensExcludeToolUsePromptTokens(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatGemini,
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "ok"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        151,
			ToolUsePromptTokenCount: 18329,
			CandidatesTokenCount:    1089,
			ThoughtsTokenCount:      1120,
			TotalTokenCount:         20689,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiChatHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 18480, usage.PromptTokens)
	require.Equal(t, 2209, usage.CompletionTokens)
	require.Equal(t, 20689, usage.TotalTokens)
	require.Equal(t, 1120, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestGeminiStreamHandlerCompletionTokensExcludeToolUsePromptTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	chunk := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "partial"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        151,
			ToolUsePromptTokenCount: 18329,
			CandidatesTokenCount:    1089,
			ThoughtsTokenCount:      1120,
			TotalTokenCount:         20689,
		},
	}

	chunkData, err := common.Marshal(chunk)
	require.NoError(t, err)

	streamBody := []byte("data: " + string(chunkData) + "\n" + "data: [DONE]\n")
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(streamBody)),
	}

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 18480, usage.PromptTokens)
	require.Equal(t, 2209, usage.CompletionTokens)
	require.Equal(t, 20689, usage.TotalTokens)
	require.Equal(t, 1120, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestGeminiTextGenerationHandlerPromptTokensIncludeToolUsePromptTokens(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-flash-preview:generateContent", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "ok"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        151,
			ToolUsePromptTokenCount: 18329,
			CandidatesTokenCount:    1089,
			ThoughtsTokenCount:      1120,
			TotalTokenCount:         20689,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiTextGenerationHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 18480, usage.PromptTokens)
	require.Equal(t, 2209, usage.CompletionTokens)
	require.Equal(t, 20689, usage.TotalTokens)
	require.Equal(t, 1120, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestGeminiChatHandlerUsesEstimatedPromptTokensWhenUsagePromptMissing(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatGemini,
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "ok"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        0,
			ToolUsePromptTokenCount: 0,
			CandidatesTokenCount:    90,
			ThoughtsTokenCount:      10,
			TotalTokenCount:         110,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiChatHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.Equal(t, 100, usage.CompletionTokens)
	require.Equal(t, 110, usage.TotalTokens)
}

func TestGeminiStreamHandlerUsesEstimatedPromptTokensWhenUsagePromptMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	chunk := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "partial"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        0,
			ToolUsePromptTokenCount: 0,
			CandidatesTokenCount:    90,
			ThoughtsTokenCount:      10,
			TotalTokenCount:         110,
		},
	}

	chunkData, err := common.Marshal(chunk)
	require.NoError(t, err)

	streamBody := []byte("data: " + string(chunkData) + "\n" + "data: [DONE]\n")
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(streamBody)),
	}

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.Equal(t, 100, usage.CompletionTokens)
	require.Equal(t, 110, usage.TotalTokens)
}

func TestGeminiTextGenerationHandlerUsesEstimatedPromptTokensWhenUsagePromptMissing(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-flash-preview:generateContent", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "ok"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        0,
			ToolUsePromptTokenCount: 0,
			CandidatesTokenCount:    90,
			ThoughtsTokenCount:      10,
			TotalTokenCount:         110,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiTextGenerationHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.Equal(t, 100, usage.CompletionTokens)
	require.Equal(t, 110, usage.TotalTokens)
}

func TestGeminiChatHandlerMissingUsageMetadataBuildsEstimatedBillingUsage(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatGemini,
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	body := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiChatHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.NotNil(t, usage.BillingUsage)
	require.True(t, usage.BillingUsage.Estimated)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, usage.BillingUsage.Source)
	require.Equal(t, dto.BillingUsageSemanticGemini, usage.BillingUsage.Semantic)
	require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, usage.PromptTokens, usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	require.Equal(t, usage.CompletionTokens, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	require.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

func TestGeminiStreamHandlerPromptOnlyUsageMetadataEstimatesCompletionTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	for _, tc := range []struct {
		name      string
		part      dto.GeminiPart
		usageText string
	}{
		{"text", dto.GeminiPart{Text: "partial streamed answer before disconnect"}, "partial streamed answer before disconnect"},
		// An agent turn that only calls a tool still produced output tokens.
		{"function call", dto.GeminiPart{FunctionCall: &dto.FunctionCall{FunctionName: "read_file", Arguments: map[string]any{"path": "main.go"}}}, `read_file{"path":"main.go"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

			info := &relaycommon.RelayInfo{
				OriginModelName: "gemini-3-flash-preview",
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "gemini-3-flash-preview",
				},
			}
			info.SetEstimatePromptTokens(20)

			// Simulates a client aborting the stream before the final chunk: output was
			// streamed but the last observed usageMetadata only carries prompt tokens.
			chunk := dto.GeminiChatResponse{
				Candidates: []dto.GeminiChatCandidate{
					{
						Content: dto.GeminiChatContent{
							Role:  "model",
							Parts: []dto.GeminiPart{tc.part},
						},
					},
				},
				UsageMetadata: dto.GeminiUsageMetadata{
					PromptTokenCount: 151,
					TotalTokenCount:  151,
				},
			}

			chunkData, err := common.Marshal(chunk)
			require.NoError(t, err)

			streamBody := []byte("data: " + string(chunkData) + "\n" + "data: [DONE]\n")
			resp := &http.Response{
				Body: io.NopCloser(bytes.NewReader(streamBody)),
			}

			usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
				return true
			})
			require.Nil(t, newAPIError)
			require.NotNil(t, usage)
			require.Equal(t, 151, usage.PromptTokens)
			require.Equal(t, tokenkit.Estimate("gemini-3-flash-preview", tc.usageText), usage.CompletionTokens)
			require.Greater(t, usage.CompletionTokens, 0)
			require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
			require.NotNil(t, usage.BillingUsage)
			require.True(t, usage.BillingUsage.Estimated)
			require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
			require.Equal(t, usage.CompletionTokens, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
		})
	}
}

func TestGeminiChatHandlerPromptOnlyUsageMetadataEstimatesCompletionTokens(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatGemini,
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}

	payload := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Role: "model",
					Parts: []dto.GeminiPart{
						{Text: "answer text without candidate token count"},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount: 151,
			TotalTokenCount:  151,
		},
	}

	body, err := common.Marshal(payload)
	require.NoError(t, err)

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	usage, newAPIError := GeminiChatHandler(c, info, resp)
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 151, usage.PromptTokens)
	require.Greater(t, usage.CompletionTokens, 0)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.NotNil(t, usage.BillingUsage)
	require.True(t, usage.BillingUsage.Estimated)
}

func TestGeminiStreamHandlerEmptyUsageMetadataBuildsEstimatedBillingUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-flash-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3-flash-preview",
		},
	}
	info.SetEstimatePromptTokens(20)

	streamBody := []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{}}\n" + "data: [DONE]\n")
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(streamBody)),
	}

	usage, newAPIError := geminiStreamHandler(c, info, resp, func(_ string, _ *dto.GeminiChatResponse) bool {
		return true
	})
	require.Nil(t, newAPIError)
	require.NotNil(t, usage)
	require.Equal(t, 20, usage.PromptTokens)
	require.NotNil(t, usage.BillingUsage)
	require.True(t, usage.BillingUsage.Estimated)
	require.Equal(t, dto.BillingUsageSourceGeminiChat, usage.BillingUsage.Source)
	require.NotNil(t, usage.BillingUsage.GeminiUsageMetadata)
	require.Equal(t, usage.PromptTokens, usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	require.Equal(t, usage.CompletionTokens, usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	require.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

// Google Search grounding records the unique trimmed non-empty queries
// (webSearchQueries and imageSearchQueries of every candidate, across every
// stream frame) under google_search and one grounded prompt when any query
// exists; the tool price table decides which of the two a model pays for.
func TestGeminiHandlersCountGroundingQueries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})
	const text = `"content":{"role":"model","parts":[{"text":"ok"}]}`
	grounded := func(queries int) map[string]int {
		return map[string]int{dto.BuildInToolGoogleSearch: queries, relaycommon.GoogleSearchGroundedPromptTool: 1}
	}

	tests := []struct {
		name   string
		stream bool
		frames []string
		want   map[string]int
	}{
		{
			name:   "native non-stream",
			frames: []string{`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["Shanghai weather today","current weather in Shanghai"],"groundingChunks":[{"web":{"uri":"https://a.example"}}]}}]}`},
			want:   grounded(2),
		},
		{
			name:   "duplicates, whitespace and empty queries",
			frames: []string{`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["go release",""," go release ","   ","tokyo"]}}]}`},
			want:   grounded(2),
		},
		{
			name:   "union across candidates",
			frames: []string{`{"candidates":[{"index":0,` + text + `,"groundingMetadata":{"webSearchQueries":["q1","q2"]}},{"index":1,` + text + `,"groundingMetadata":{"webSearchQueries":["q2","q3"]}}]}`},
			want:   grounded(3),
		},
		{
			name:   "image queries count like web queries",
			frames: []string{`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["cat breeds","orange cat"],"imageSearchQueries":["orange tabby cat photo","orange cat"]}}]}`},
			want:   grounded(3),
		},
		{
			name:   "only empty queries",
			frames: []string{`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["","  "],"imageSearchQueries":[""]}}]}`},
			want:   map[string]int{},
		},
		{
			name:   "no grounding metadata",
			frames: []string{`{"candidates":[{` + text + `,"finishReason":"STOP"}]}`},
			want:   map[string]int{},
		},
		{
			name:   "stream union across frames",
			stream: true,
			frames: []string{
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q1"]}}]}`,
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q2","q1 "]}}]}`,
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q3"]}}]}`,
			},
			want: grounded(3),
		},
		{
			name:   "stream repeats are not added up",
			stream: true,
			frames: []string{
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q1","q2"]}}]}`,
				`{"candidates":[{` + text + `,"groundingMetadata":{"webSearchQueries":["q1","q2"]}}]}`,
			},
			want: grounded(2),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.7-flash:generateContent", nil)
			info := &relaycommon.RelayInfo{
				OriginModelName: "gemini-3.7-flash",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.7-flash"},
			}
			var apiErr *types.NewAPIError
			if tt.stream {
				body := "data: " + strings.Join(tt.frames, "\ndata: ") + "\ndata: [DONE]\n"
				_, apiErr = geminiStreamHandler(c, info, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, func(string, *dto.GeminiChatResponse) bool {
					return true
				})
			} else {
				_, apiErr = GeminiTextGenerationHandler(c, info, &http.Response{Body: io.NopCloser(strings.NewReader(tt.frames[0]))})
			}
			require.Nil(t, apiErr)
			counts := map[string]int{}
			if info.ResponsesUsageInfo != nil {
				for name, tool := range info.ResponsesUsageInfo.BuiltInTools {
					counts[name] = tool.CallCount
				}
			}
			assert.Equal(t, tt.want, counts)
		})
	}
}

func TestGeminiTextGenerationHandlerRejectsBlockedResponseWithoutOutput(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	imagePart := dto.GeminiPart{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "aGk="}}
	cases := []struct {
		name          string
		finishReason  string
		parts         []dto.GeminiPart
		promptBlocked bool
		wantBlocked   bool
	}{
		{name: "blocked prompt without candidates", finishReason: "PROHIBITED_CONTENT", promptBlocked: true, wantBlocked: true},
		{name: "prohibited content without output", finishReason: "PROHIBITED_CONTENT", wantBlocked: true},
		{name: "image safety without output", finishReason: "IMAGE_SAFETY", wantBlocked: true},
		{name: "no image without output", finishReason: "NO_IMAGE", wantBlocked: true},
		{name: "lowercase reason without output", finishReason: "prohibited_content", wantBlocked: true},
		{name: "blocked reason with image output", finishReason: "IMAGE_SAFETY", parts: []dto.GeminiPart{imagePart}},
		{name: "normal stop without output", finishReason: "STOP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:generateContent", nil)
			info := &relaycommon.RelayInfo{
				OriginModelName: "gemini-3-pro-image-preview",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3-pro-image"},
			}
			payload := dto.GeminiChatResponse{
				Candidates: []dto.GeminiChatCandidate{{
					Content:      dto.GeminiChatContent{Role: "model", Parts: tc.parts},
					FinishReason: &tc.finishReason,
				}},
				UsageMetadata: dto.GeminiUsageMetadata{PromptTokenCount: 481, TotalTokenCount: 481},
			}
			if tc.promptBlocked {
				payload = dto.GeminiChatResponse{PromptFeedback: &dto.GeminiChatPromptFeedback{BlockReason: &tc.finishReason}}
			}
			body, err := common.Marshal(payload)
			require.NoError(t, err)

			usage, newAPIError := GeminiTextGenerationHandler(c, info, &http.Response{Body: io.NopCloser(bytes.NewReader(body))})

			if !tc.wantBlocked {
				require.Nil(t, newAPIError)
				require.NotNil(t, usage)
				assert.NotEmpty(t, recorder.Body.Bytes())
				return
			}
			require.NotNil(t, newAPIError)
			assert.Equal(t, http.StatusBadRequest, newAPIError.StatusCode)
			assert.Equal(t, types.ErrorCodePromptBlocked, newAPIError.GetErrorCode())
			assert.Contains(t, newAPIError.Error(), strings.ToUpper(tc.finishReason))
			assert.True(t, info.PerformanceBusinessRejection)
			assert.Empty(t, recorder.Body.Bytes())
		})
	}
}

func TestGeminiChatAndStreamHandlersRejectBlockedResponsesWithoutOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	reason := "PROHIBITED_CONTENT"
	finishBlocked := dto.GeminiChatResponse{
		Candidates:    []dto.GeminiChatCandidate{{Content: dto.GeminiChatContent{Role: "model"}, FinishReason: &reason}},
		UsageMetadata: dto.GeminiUsageMetadata{PromptTokenCount: 481, TotalTokenCount: 481},
	}
	promptBlocked := dto.GeminiChatResponse{PromptFeedback: &dto.GeminiChatPromptFeedback{BlockReason: &reason}}
	withOutput := dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{Content: dto.GeminiChatContent{Role: "model", Parts: []dto.GeminiPart{{Text: "partial"}}}}},
	}
	type handler func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)
	cases := []struct {
		name        string
		handler     handler
		stream      bool
		frames      []dto.GeminiChatResponse
		wantBlocked bool
	}{
		{name: "chat blocked finish reason", handler: GeminiChatHandler, frames: []dto.GeminiChatResponse{finishBlocked}, wantBlocked: true},
		{name: "chat blocked prompt", handler: GeminiChatHandler, frames: []dto.GeminiChatResponse{promptBlocked}, wantBlocked: true},
		{name: "chat stream blocked finish reason", handler: GeminiChatStreamHandler, stream: true, frames: []dto.GeminiChatResponse{finishBlocked}, wantBlocked: true},
		{name: "chat stream blocked prompt", handler: GeminiChatStreamHandler, stream: true, frames: []dto.GeminiChatResponse{promptBlocked}, wantBlocked: true},
		{name: "responses blocked finish reason", handler: GeminiResponsesHandler, frames: []dto.GeminiChatResponse{finishBlocked}, wantBlocked: true},
		{name: "responses stream blocked finish reason", handler: GeminiResponsesStreamHandler, stream: true, frames: []dto.GeminiChatResponse{finishBlocked}, wantBlocked: true},
		{name: "native stream blocked finish reason", handler: GeminiTextGenerationStreamHandler, stream: true, frames: []dto.GeminiChatResponse{finishBlocked}, wantBlocked: true},
		{name: "native stream blocked prompt", handler: GeminiTextGenerationStreamHandler, stream: true, frames: []dto.GeminiChatResponse{promptBlocked}, wantBlocked: true},
		{name: "native stream blocked after output", handler: GeminiTextGenerationStreamHandler, stream: true, frames: []dto.GeminiChatResponse{withOutput, finishBlocked}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:     types.RelayFormatOpenAI,
				OriginModelName: "gemini-3-pro-image-preview",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3-pro-image"},
			}
			var body bytes.Buffer
			for _, frame := range tc.frames {
				data, err := common.Marshal(frame)
				require.NoError(t, err)
				if tc.stream {
					body.WriteString("data: ")
				}
				body.Write(data)
				body.WriteString("\n")
			}
			if tc.stream {
				body.WriteString("data: [DONE]\n")
			}

			_, newAPIError := tc.handler(c, info, &http.Response{Body: io.NopCloser(&body)})

			if !tc.wantBlocked {
				require.Nil(t, newAPIError)
				assert.Contains(t, recorder.Body.String(), "partial")
				return
			}
			require.NotNil(t, newAPIError)
			assert.Equal(t, http.StatusBadRequest, newAPIError.StatusCode)
			assert.Equal(t, types.ErrorCodePromptBlocked, newAPIError.GetErrorCode())
			assert.Contains(t, newAPIError.Error(), reason)
			assert.True(t, info.PerformanceBusinessRejection)
			assert.Empty(t, recorder.Body.String())
		})
	}
}
