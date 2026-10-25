package relay

import "testing"

func TestIsMiniMaxH3OpenAIVideo(t *testing.T) {
	tests := map[string]bool{
		"Minimax-H3-768p-933-10s": true,
		"Minimax-H3-768p-933-15s": true,
		" MiniMax-Hailuo-2.3 ":    false,
		"MiniMax-Hailuo-02":       false,
		"Minimax-H3-other-10s":    false,
	}

	for modelName, want := range tests {
		if got := isMiniMaxH3OpenAIVideo(modelName); got != want {
			t.Fatalf("isMiniMaxH3OpenAIVideo(%q) = %v, want %v", modelName, got, want)
		}
	}
}
