package service

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/types"
)

// This is the operator-requested exception for banana channel 92. Keep the
// general manual-status policy intact, including other channels' 402 errors.
func IsChannelQuotaExhausted(channelID int, err *types.NewAPIError) bool {
	if channelID != 92 || err == nil || err.StatusCode != http.StatusPaymentRequired {
		return false
	}
	// RelayErrorHandler wraps actual upstream HTTP error responses as OpenAI
	// errors. Local account/token pre-consumption failures are NewAPI errors.
	if err.GetErrorType() != types.ErrorTypeOpenAIError {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "user quota is not enough")
}
