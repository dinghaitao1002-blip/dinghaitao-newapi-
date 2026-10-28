package common

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ChannelCircuitFailureThreshold   = GetEnvOrDefault("CHANNEL_CIRCUIT_FAILURE_THRESHOLD", 10)
	ChannelCircuitForbiddenThreshold = GetEnvOrDefault("CHANNEL_CIRCUIT_FORBIDDEN_THRESHOLD", 10)
	ChannelCircuitInternalThreshold  = GetEnvOrDefault("CHANNEL_CIRCUIT_INTERNAL_THRESHOLD", 10)
	ChannelCircuitFailureWindow      = time.Duration(GetEnvOrDefault("CHANNEL_CIRCUIT_FAILURE_WINDOW_SECONDS", 1800)) * time.Second
	ChannelCircuitCooldown           = time.Duration(GetEnvOrDefault("CHANNEL_CIRCUIT_COOLDOWN_SECONDS", 600)) * time.Second
)

type channelCircuitMemoryState struct {
	FailureClass string
	Count        int
	LastFailure  time.Time
	OpenUntil    time.Time
}

var channelCircuitMemory = struct {
	sync.Mutex
	states map[string]channelCircuitMemoryState
}{states: make(map[string]channelCircuitMemoryState)}

func IsChannelCircuitFailureStatus(statusCode int) bool {
	return statusCode == 403 || statusCode == 500 || statusCode == 429 || statusCode == 502 || statusCode == 504
}

// An explicit input-image fetch error describes this request's image source,
// not the health of the generation channel. Match only the known 400 response
// prefix; a URL or a generic upstream timeout must not exempt a channel failure.
func isChannelCircuitInputImageFetchError(statusCode int, errText string) bool {
	if statusCode != 400 {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(errText))
	message = strings.TrimSpace(strings.TrimPrefix(message, "status_code=400,"))
	return strings.HasPrefix(message, "image_url fetch failed:")
}

func channelCircuitFailureClass(statusCode int, errText string) (string, int, bool) {
	lowerText := strings.ToLower(errText)
	if statusCode == 422 || statusCode == 451 || statusCode == 413 ||
		strings.Contains(lowerText, "content moderation") || strings.Contains(lowerText, "content policy") ||
		strings.Contains(lowerText, "content_filter") || strings.Contains(lowerText, "safety system") ||
		strings.Contains(lowerText, "内容审核") || strings.Contains(lowerText, "防护限制") {
		return "", 0, false
	}
	if statusCode == 403 {
		threshold := ChannelCircuitForbiddenThreshold
		if threshold < 1 {
			threshold = 10
		}
		return "forbidden", threshold, true
	}
	if statusCode == 500 {
		threshold := ChannelCircuitInternalThreshold
		if threshold < 1 {
			threshold = 10
		}
		return "internal", threshold, true
	}
	if statusCode == 429 || statusCode == 502 || statusCode == 503 || statusCode == 504 {
		threshold := ChannelCircuitFailureThreshold
		if threshold < 1 {
			threshold = 10
		}
		return fmt.Sprintf("upstream_%d", statusCode), threshold, true
	}
	if statusCode == 400 {
		lowerText := strings.ToLower(errText)
		if strings.Contains(lowerText, "timeout") || strings.Contains(lowerText, "timed out") || strings.Contains(lowerText, "超时") || strings.Contains(lowerText, "deadline exceeded") {
			threshold := ChannelCircuitFailureThreshold
			if threshold < 1 {
				threshold = 10
			}
			return "transient", threshold, true
		}
	}
	return "", 0, false
}

func channelCircuitIdentity(channelID int, modelName string) string {
	hash := sha256.Sum256([]byte(modelName))
	return fmt.Sprintf("%d:%s", channelID, hex.EncodeToString(hash[:8]))
}

func channelCircuitKeys(channelID int, modelName string) (string, string) {
	identity := channelCircuitIdentity(channelID, modelName)
	return "channel_circuit:fail:" + identity, "channel_circuit:open:" + identity
}

func normalizeChannelCircuitDurations() (time.Duration, time.Duration) {
	failureWindow := ChannelCircuitFailureWindow
	if failureWindow <= 0 {
		failureWindow = 30 * time.Minute
	}
	cooldown := ChannelCircuitCooldown
	if cooldown <= 0 {
		cooldown = 10 * time.Minute
	}
	return failureWindow, cooldown
}

func recordChannelCircuitFailureMemory(identity string, failureClass string, threshold int, now time.Time) (int, bool) {
	failureWindow, cooldown := normalizeChannelCircuitDurations()
	channelCircuitMemory.Lock()
	defer channelCircuitMemory.Unlock()
	state := channelCircuitMemory.states[identity]
	if now.Before(state.OpenUntil) {
		// Responses from already-in-flight requests must not extend or clear
		// the routing cooldown established by the tenth failure.
		return state.Count, false
	}
	if !state.OpenUntil.IsZero() && failureWindow < cooldown+time.Minute {
		failureWindow = cooldown + time.Minute
	}
	if state.FailureClass != failureClass || (!state.LastFailure.IsZero() && now.Sub(state.LastFailure) > failureWindow) {
		state.Count = 0
	}
	state.FailureClass = failureClass
	state.Count++
	state.LastFailure = now
	count := state.Count
	tripped := count >= threshold
	if tripped {
		state.Count = threshold - 1
		state.OpenUntil = now.Add(cooldown)
	}
	channelCircuitMemory.states[identity] = state
	return count, tripped
}

// RecordChannelCircuitFailure tracks consecutive upstream failures by class:
// 403 uses the shorter forbidden threshold, 500 uses its own internal-error
// threshold, and 429/502/504 share the transient threshold. Switching class
// restarts the sequence.
func RecordChannelCircuitFailure(channelID int, modelName string, statusCode int, errText ...string) (int, bool) {
	if channelID <= 0 || modelName == "" {
		return 0, false
	}
	text := ""
	if len(errText) > 0 {
		text = errText[0]
	}
	if isChannelCircuitInputImageFetchError(statusCode, text) {
		// Neither increment nor reset channel health state: interleaved bad
		// image URLs must not hide real failures or release an active cooldown.
		return 0, false
	}
	failureKey, openKey := channelCircuitKeys(channelID, modelName)
	failureClass, threshold, tracked := channelCircuitFailureClass(statusCode, text)
	if !tracked {
		if RedisEnabled && RDB != nil {
			_, _ = RDB.Eval(context.Background(), `if redis.call('EXISTS', KEYS[2]) == 0 then return redis.call('DEL', KEYS[1]) end return 0`, []string{failureKey, openKey}).Result()
		}
		channelCircuitMemory.Lock()
		state := channelCircuitMemory.states[channelCircuitIdentity(channelID, modelName)]
		if time.Now().Before(state.OpenUntil) {
			channelCircuitMemory.Unlock()
			return state.Count, false
		}
		state.Count = 0
		state.FailureClass = ""
		state.LastFailure = time.Time{}
		channelCircuitMemory.states[channelCircuitIdentity(channelID, modelName)] = state
		channelCircuitMemory.Unlock()
		return 0, false
	}

	failureWindow, cooldown := normalizeChannelCircuitDurations()
	if RedisEnabled && RDB != nil {
		const script = `
if redis.call('EXISTS', KEYS[2]) == 1 then return {0, 0} end
local count = 1
local previous = redis.call('GET', KEYS[1])
if previous then
  local separator = string.find(previous, ':')
  if separator then
    local previous_class = string.sub(previous, 1, separator - 1)
    local previous_count = tonumber(string.sub(previous, separator + 1)) or 0
    if previous_class == ARGV[1] then
      count = previous_count + 1
    end
  end
end
redis.call('SET', KEYS[1], ARGV[1] .. ':' .. count, 'EX', ARGV[2])
if count >= tonumber(ARGV[3]) then
  redis.call('SET', KEYS[2], '1', 'EX', ARGV[4])
  redis.call('SET', KEYS[1], ARGV[1] .. ':' .. (tonumber(ARGV[3]) - 1), 'EX', math.max(tonumber(ARGV[2]), tonumber(ARGV[4]) + 60))
  return {count, 1}
end
return {count, 0}`
		result, err := RDB.Eval(
			context.Background(), script, []string{failureKey, openKey},
			failureClass, int(failureWindow.Seconds()), threshold, int(cooldown.Seconds()),
		).Result()
		if err == nil {
			if values, ok := result.([]interface{}); ok && len(values) == 2 {
				count, _ := strconv.Atoi(fmt.Sprint(values[0]))
				tripped := fmt.Sprint(values[1]) == "1"
				return count, tripped
			}
		}
	}
	return recordChannelCircuitFailureMemory(channelCircuitIdentity(channelID, modelName), failureClass, threshold, time.Now())
}

// ResetChannelCircuitOnSuccess clears a consecutive failure sequence, but never
// releases an active routing cooldown due to a late in-flight success.
func ResetChannelCircuitOnSuccess(channelID int, modelName string) {
	if channelID <= 0 || modelName == "" {
		return
	}
	failureKey, openKey := channelCircuitKeys(channelID, modelName)
	if RedisEnabled && RDB != nil {
		_, err := RDB.Eval(context.Background(), `if redis.call('EXISTS', KEYS[2]) == 0 then return redis.call('DEL', KEYS[1]) end return 0`, []string{failureKey, openKey}).Result()
		if err == nil {
			return
		}
	}
	channelCircuitMemory.Lock()
	identity := channelCircuitIdentity(channelID, modelName)
	if !time.Now().Before(channelCircuitMemory.states[identity].OpenUntil) {
		delete(channelCircuitMemory.states, identity)
	}
	channelCircuitMemory.Unlock()
}

func isChannelCircuitOpenMemory(identity string, now time.Time) bool {
	channelCircuitMemory.Lock()
	defer channelCircuitMemory.Unlock()
	state, ok := channelCircuitMemory.states[identity]
	if !ok || state.OpenUntil.IsZero() {
		return false
	}
	if !now.Before(state.OpenUntil) {
		// Keep the previous failure sequence for the first recovery outcome.
		return false
	}
	return true
}

func IsChannelCircuitOpen(channelID int, modelName string) bool {
	if channelID <= 0 || modelName == "" {
		return false
	}
	_, openKey := channelCircuitKeys(channelID, modelName)
	if RedisEnabled && RDB != nil {
		exists, err := RDB.Exists(context.Background(), openKey).Result()
		if err == nil {
			return exists > 0
		}
	}
	return isChannelCircuitOpenMemory(channelCircuitIdentity(channelID, modelName), time.Now())
}

// FilterUsableChannelIDs removes channels whose circuit is open for this model.
// Redis MGET keeps the routing path to one cache round trip regardless of the
// number of candidate channels.
func FilterUsableChannelIDs(channelIDs []int, modelName string) []int {
	if len(channelIDs) == 0 || modelName == "" {
		return channelIDs
	}
	if RedisEnabled && RDB != nil {
		keys := make([]string, len(channelIDs))
		for i, channelID := range channelIDs {
			_, keys[i] = channelCircuitKeys(channelID, modelName)
		}
		values, err := RDB.MGet(context.Background(), keys...).Result()
		if err == nil {
			filtered := make([]int, 0, len(channelIDs))
			for i, value := range values {
				if value == nil {
					filtered = append(filtered, channelIDs[i])
				}
			}
			return filtered
		}
	}
	filtered := make([]int, 0, len(channelIDs))
	now := time.Now()
	for _, channelID := range channelIDs {
		if !isChannelCircuitOpenMemory(channelCircuitIdentity(channelID, modelName), now) {
			filtered = append(filtered, channelID)
		}
	}
	return filtered
}
