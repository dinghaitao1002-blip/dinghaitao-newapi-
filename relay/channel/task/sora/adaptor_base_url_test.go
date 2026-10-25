package sora

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestNormalizeOpenAIBaseURL(t *testing.T) {
	tests := map[string]string{
		"https://example.com":      "https://example.com",
		"https://example.com/":     "https://example.com",
		"https://example.com/v1":   "https://example.com",
		"https://example.com/v1/":  "https://example.com",
		" https://example.com/v1 ": "https://example.com",
	}

	for input, want := range tests {
		if got := normalizeOpenAIBaseURL(input); got != want {
			t.Fatalf("normalizeOpenAIBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestConvertToOpenAIVideoUsesPublicTaskID(t *testing.T) {
	task := &model.Task{
		TaskID: "task_public",
		Data:   []byte(`{"id":"task_upstream","task_id":"task_upstream","status":"queued"}`),
	}

	data, err := (&TaskAdaptor{}).ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"id":"task_public"`) || !strings.Contains(got, `"task_id":"task_public"`) {
		t.Fatalf("public task id was not applied to both fields: %s", got)
	}
}
