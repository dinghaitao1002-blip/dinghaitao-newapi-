package model

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func TestNotStartedVideoTaskIsQueued(t *testing.T) {
	if got := TaskStatusNotStart.ToVideoStatus(); got != dto.VideoStatusQueued {
		t.Fatalf("TaskStatusNotStart.ToVideoStatus() = %q, want %q", got, dto.VideoStatusQueued)
	}
}
