package common

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

const (
	ModelConcurrencyOptionKey         = "ModelConcurrencyConfig"
	ModelConcurrencyLeaseTTL          = 2 * time.Minute
	ModelConcurrencyHeartbeatInterval = 20 * time.Second
)

type ModelConcurrencyUserLimit struct {
	UserID        int `json:"user_id"`
	MaxConcurrent int `json:"max_concurrent"`
}

type ModelConcurrencyRule struct {
	Model            string                      `json:"model"`
	Enabled          bool                        `json:"enabled"`
	MaxConcurrent    int                         `json:"max_concurrent"`
	DefaultUserLimit int                         `json:"default_user_limit"`
	OtherUsersLimit  int                         `json:"other_users_limit"`
	UserLimits       []ModelConcurrencyUserLimit `json:"user_limits"`
}

type ModelConcurrencyConfig struct {
	Enabled bool                   `json:"enabled"`
	Rules   []ModelConcurrencyRule `json:"rules"`
}

func (config ModelConcurrencyConfig) Validate() error {
	if len(config.Rules) > 1000 {
		return errors.New("at most 1000 model concurrency rules are allowed")
	}
	models := make(map[string]bool, len(config.Rules))
	for _, rule := range config.Rules {
		if rule.Model == "" || strings.TrimSpace(rule.Model) != rule.Model || len(rule.Model) > 256 {
			return errors.New("model must be a non-empty name without surrounding whitespace, at most 256 bytes")
		}
		if models[rule.Model] {
			return fmt.Errorf("duplicate model concurrency rule: %s", rule.Model)
		}
		models[rule.Model] = true
		for _, limit := range []int{rule.MaxConcurrent, rule.DefaultUserLimit, rule.OtherUsersLimit} {
			if limit < 0 || limit > 1000000 {
				return errors.New("concurrency limits must be integers between 0 and 1000000")
			}
		}
		if len(rule.UserLimits) > 1000 {
			return errors.New("at most 1000 user overrides are allowed per model")
		}
		users := make(map[int]bool, len(rule.UserLimits))
		for _, user := range rule.UserLimits {
			if user.UserID <= 0 || user.MaxConcurrent < 0 || user.MaxConcurrent > 1000000 {
				return errors.New("user IDs must be positive and concurrency limits must be between 0 and 1000000")
			}
			if users[user.UserID] {
				return fmt.Errorf("duplicate user %d for model %s", user.UserID, rule.Model)
			}
			users[user.UserID] = true
		}
	}
	return nil
}

func ParseModelConcurrencyConfig(raw string) (ModelConcurrencyConfig, error) {
	var config ModelConcurrencyConfig
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return config, errors.New("model concurrency configuration must be an object")
	}
	if err := UnmarshalJsonStr(raw, &config); err != nil {
		return config, err
	}
	// Unknown keys are rejected so a misspelled limit cannot silently disable it.
	var object map[string]RawMessage
	if err := UnmarshalJsonStr(raw, &object); err != nil {
		return config, err
	}
	for key := range object {
		if key != "enabled" && key != "rules" {
			return config, fmt.Errorf("unknown concurrency configuration field: %s", key)
		}
		if key == "enabled" && strings.TrimSpace(string(object[key])) == "null" {
			return config, errors.New("enabled must be a boolean")
		}
	}
	var rules []map[string]RawMessage
	if err := Unmarshal(object["rules"], &rules); err != nil && object["rules"] != nil {
		return config, err
	}
	for _, rule := range rules {
		for key := range rule {
			if !slices.Contains([]string{"model", "enabled", "max_concurrent", "default_user_limit", "other_users_limit", "user_limits"}, key) {
				return config, fmt.Errorf("unknown model concurrency field: %s", key)
			}
			if key != "user_limits" && strings.TrimSpace(string(rule[key])) == "null" {
				return config, fmt.Errorf("model concurrency field cannot be null: %s", key)
			}
		}
		var users []map[string]RawMessage
		if err := Unmarshal(rule["user_limits"], &users); err != nil && rule["user_limits"] != nil {
			return config, err
		}
		for _, user := range users {
			for key := range user {
				if key != "user_id" && key != "max_concurrent" {
					return config, fmt.Errorf("unknown user concurrency field: %s", key)
				}
				if strings.TrimSpace(string(user[key])) == "null" {
					return config, fmt.Errorf("user concurrency field cannot be null: %s", key)
				}
			}
		}
	}
	if config.Rules == nil {
		config.Rules = []ModelConcurrencyRule{}
	}
	for i := range config.Rules {
		if config.Rules[i].UserLimits == nil {
			config.Rules[i].UserLimits = []ModelConcurrencyUserLimit{}
		}
	}
	return config, config.Validate()
}

func (config ModelConcurrencyConfig) Rule(model string) (ModelConcurrencyRule, bool) {
	if config.Enabled {
		for _, rule := range config.Rules {
			if rule.Enabled && rule.Model == model {
				return rule, true
			}
		}
	}
	return ModelConcurrencyRule{}, false
}

var (
	ErrModelConcurrencyExceeded     = errors.New("model concurrency limit exceeded")
	ErrModelConcurrencyUnavailable  = errors.New("model concurrency storage unavailable")
	ErrModelConcurrencyLeaseExpired = errors.New("model concurrency lease expired")
)

type ModelConcurrencyLimitError struct {
	Scope   string `json:"scope"`
	Current int    `json:"current"`
	Limit   int    `json:"limit"`
}

func (err *ModelConcurrencyLimitError) Error() string {
	return fmt.Sprintf("%s: %s (%d/%d)", ErrModelConcurrencyExceeded, err.Scope, err.Current, err.Limit)
}
func (err *ModelConcurrencyLimitError) Unwrap() error { return ErrModelConcurrencyExceeded }

type ModelConcurrencyLeaseIdentity struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	UserID     int    `json:"user_id"`
	Storage    string `json:"storage"`
	Persistent bool   `json:"persistent"`
}

type ModelConcurrencyUserUsage struct {
	UserID  int `json:"user_id"`
	Current int `json:"current"`
}

type ModelConcurrencyUsage struct {
	Model      string                      `json:"model"`
	Total      int                         `json:"total"`
	OtherUsers int                         `json:"other_users"`
	Users      []ModelConcurrencyUserUsage `json:"users"`
}

type modelConcurrencyMemoryLease struct {
	userID  int
	expires time.Time
}

// The Redis backend uses one model lease set and one per-user count hash in
// the same hash slot. Admission checks all three scopes in one Lua execution.
// "Other users" is computed from the CURRENT override set, so editing which
// users are special never loses or misclassifies existing leases.
type ModelConcurrencyLimiter struct {
	redis  *redis.Client
	ttl    time.Duration
	now    func() time.Time
	mu     sync.Mutex
	memory map[string]map[string]modelConcurrencyMemoryLease
}

func NewModelConcurrencyLimiter(client *redis.Client, ttl time.Duration) *ModelConcurrencyLimiter {
	if ttl <= 0 {
		ttl = ModelConcurrencyLeaseTTL
	}
	return &ModelConcurrencyLimiter{redis: client, ttl: ttl, now: time.Now, memory: make(map[string]map[string]modelConcurrencyMemoryLease)}
}

var modelConcurrencyMemory = NewModelConcurrencyLimiter(nil, ModelConcurrencyLeaseTTL)

func ModelConcurrencyStorage() string {
	if RedisEnabled {
		return "redis"
	}
	return "memory"
}

func AcquireModelConcurrency(ctx context.Context, model string, userID int, rule ModelConcurrencyRule) (*ModelConcurrencyLease, error) {
	if RedisEnabled {
		if RDB == nil {
			return nil, ErrModelConcurrencyUnavailable
		}
		return NewModelConcurrencyLimiter(RDB, ModelConcurrencyLeaseTTL).Acquire(ctx, model, userID, rule)
	}
	return modelConcurrencyMemory.Acquire(ctx, model, userID, rule)
}

func RestoreModelConcurrencyLease(identity ModelConcurrencyLeaseIdentity) (*ModelConcurrencyLease, error) {
	_, uuidErr := uuid.Parse(identity.ID)
	if (uuidErr != nil && !durableModelConcurrencyID.MatchString(identity.ID)) || identity.UserID <= 0 || identity.Model == "" || len(identity.Model) > 256 {
		return nil, errors.New("invalid concurrency lease identity")
	}
	limiter := modelConcurrencyMemory
	switch identity.Storage {
	case "redis":
		if RDB == nil {
			return nil, ErrModelConcurrencyUnavailable
		}
		limiter = NewModelConcurrencyLimiter(RDB, ModelConcurrencyLeaseTTL)
	case "memory":
	default:
		return nil, errors.New("invalid concurrency lease storage")
	}
	return &ModelConcurrencyLease{limiter: limiter, identity: identity}, nil
}

func GetModelConcurrencyUsage(ctx context.Context, rules []ModelConcurrencyRule) ([]ModelConcurrencyUsage, error) {
	limiter := modelConcurrencyMemory
	if RedisEnabled {
		if RDB == nil {
			return nil, ErrModelConcurrencyUnavailable
		}
		limiter = NewModelConcurrencyLimiter(RDB, ModelConcurrencyLeaseTTL)
	}
	usage := make([]ModelConcurrencyUsage, 0, len(rules))
	for _, rule := range rules {
		row, err := limiter.Usage(ctx, rule)
		if err != nil {
			return nil, err
		}
		usage = append(usage, row)
	}
	return usage, nil
}

type ModelConcurrencyLease struct {
	mu       sync.Mutex
	limiter  *ModelConcurrencyLimiter
	identity ModelConcurrencyLeaseIdentity
}

func (lease *ModelConcurrencyLease) Identity() ModelConcurrencyLeaseIdentity {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.identity
}

const modelConcurrencyDurableScore = int64(9007199254740991)
const modelConcurrencyDurableModelsKey = "model-concurrency:v1:{model-concurrency}:durable-models"

var durableModelConcurrencyID = regexp.MustCompile(`^(task|image):[a-zA-Z0-9_-]{1,128}$`)

func (limiter *ModelConcurrencyLimiter) keys(model string) []string {
	hash := sha256.Sum256([]byte(model))
	prefix := fmt.Sprintf("model-concurrency:v1:{model-concurrency}:%x:", hash)
	return []string{prefix + "leases", prefix + "users"}
}

// Every operation prunes expired leases and adjusts the corresponding user
// counters atomically. Server TIME avoids clock differences between replicas.
const modelConcurrencyRedisPrune = `
local tm = redis.call('TIME')
local now = tonumber(tm[1])*1000 + math.floor(tonumber(tm[2])/1000)
local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now)
for _, member in ipairs(expired) do
  local uid = string.match(member, '^(%d+):')
  if redis.call('ZREM', KEYS[1], member) == 1 and uid then
    if redis.call('HINCRBY', KEYS[2], uid, -1) <= 0 then redis.call('HDEL', KEYS[2], uid) end
  end
end
`

// A model containing durable task slots must not disappear when a short-lived
// HTTP lease is acquired or renewed. Durable tasks are reclaimed from actual
// persisted task state by the runtime reconciler, never by an HTTP timeout.
const modelConcurrencyRedisExpiry = `
local last = redis.call('ZREVRANGE', KEYS[1], 0, 0, 'WITHSCORES')
if last[2] and tonumber(last[2]) >= 9007199254740991 then
  redis.call('PERSIST', KEYS[1])
  redis.call('PERSIST', KEYS[2])
else
  redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[3])*2)
  redis.call('PEXPIRE', KEYS[2], tonumber(ARGV[3])*2)
end
`

var modelConcurrencyAcquireScript = redis.NewScript(modelConcurrencyRedisPrune + `
local total = redis.call('ZCARD', KEYS[1])
local user = tonumber(redis.call('HGET', KEYS[2], ARGV[2]) or '0')
local others = total
for i=8,#ARGV do others = others-tonumber(redis.call('HGET', KEYS[2], ARGV[i]) or '0') end
local limits = {tonumber(ARGV[4]), tonumber(ARGV[5]), tonumber(ARGV[6])}
local counts = {total, user, others}
local scopes = {'model', 'user', 'other_users'}
for i=1,3 do
  if limits[i] > 0 and counts[i] >= limits[i] and (i ~= 3 or ARGV[7] == '1') then
    return {0, scopes[i], counts[i], limits[i]}
  end
end
if redis.call('ZADD', KEYS[1], 'NX', now+tonumber(ARGV[3]), ARGV[1]) == 1 then
  redis.call('HINCRBY', KEYS[2], ARGV[2], 1)
end
` + modelConcurrencyRedisExpiry + `
return {1, '', 0, 0}
`)

var modelConcurrencyRenewScript = redis.NewScript(modelConcurrencyRedisPrune + `
if not redis.call('ZSCORE', KEYS[1], ARGV[1]) then return 0 end
local old = tonumber(redis.call('ZSCORE', KEYS[1], ARGV[1]))
local deadline = now+tonumber(ARGV[3])
if old >= 9007199254740991 then deadline = 9007199254740991 end
redis.call('ZADD', KEYS[1], 'XX', deadline, ARGV[1])
` + modelConcurrencyRedisExpiry + `
return 1
`)

var modelConcurrencyReleaseScript = redis.NewScript(modelConcurrencyRedisPrune + `
if redis.call('ZREM', KEYS[1], ARGV[1]) == 1 then
  if redis.call('HINCRBY', KEYS[2], ARGV[2], -1) <= 0 then redis.call('HDEL', KEYS[2], ARGV[2]) end
end
if redis.call('ZCARD', KEYS[1]) == 0 then redis.call('DEL', KEYS[1], KEYS[2]) end
return 1
`)

var modelConcurrencyUsageScript = redis.NewScript(modelConcurrencyRedisPrune + `
return redis.call('HGETALL', KEYS[2])
`)

func (limiter *ModelConcurrencyLimiter) Acquire(ctx context.Context, model string, userID int, rule ModelConcurrencyRule) (*ModelConcurrencyLease, error) {
	if userID <= 0 || rule.Model != model {
		return nil, errors.New("invalid model concurrency identity")
	}
	if err := (ModelConcurrencyConfig{Rules: []ModelConcurrencyRule{rule}}).Validate(); err != nil {
		return nil, err
	}
	identity := ModelConcurrencyLeaseIdentity{ID: uuid.NewString(), Model: model, UserID: userID, Storage: "memory"}
	userLimit, other := rule.DefaultUserLimit, true
	for _, user := range rule.UserLimits {
		if user.UserID == userID {
			userLimit, other = user.MaxConcurrent, false
		}
	}
	if limiter.redis != nil {
		identity.Storage = "redis"
		args := []any{strconv.Itoa(userID) + ":" + identity.ID, userID, limiter.ttl.Milliseconds(), rule.MaxConcurrent, userLimit, rule.OtherUsersLimit, "0"}
		if other {
			args[6] = "1"
		}
		for _, user := range rule.UserLimits {
			args = append(args, user.UserID)
		}
		operationCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		result, err := modelConcurrencyAcquireScript.Run(operationCtx, limiter.redis, limiter.keys(model), args...).Slice()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrModelConcurrencyUnavailable, err)
		}
		if result[0].(int64) == 0 {
			return nil, &ModelConcurrencyLimitError{Scope: result[1].(string), Current: int(result[2].(int64)), Limit: int(result[3].(int64))}
		}
	} else {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limiter.mu.Lock()
		defer limiter.mu.Unlock()
		users := limiter.memoryUsage(model)
		counts := []int{0, users[userID], 0}
		for uid, count := range users {
			counts[0] += count
			if !slices.ContainsFunc(rule.UserLimits, func(u ModelConcurrencyUserLimit) bool { return u.UserID == uid }) {
				counts[2] += count
			}
		}
		for i, limit := range []int{rule.MaxConcurrent, userLimit, rule.OtherUsersLimit} {
			if limit > 0 && counts[i] >= limit && (i != 2 || other) {
				return nil, &ModelConcurrencyLimitError{Scope: []string{"model", "user", "other_users"}[i], Current: counts[i], Limit: limit}
			}
		}
		if limiter.memory[model] == nil {
			limiter.memory[model] = make(map[string]modelConcurrencyMemoryLease)
		}
		limiter.memory[model][identity.ID] = modelConcurrencyMemoryLease{userID: userID, expires: limiter.now().Add(limiter.ttl)}
	}
	return &ModelConcurrencyLease{limiter: limiter, identity: identity}, nil
}

// memoryUsage is called with mu held and preserves active identities when the
// limits are changed, disabled, or re-enabled.
func (limiter *ModelConcurrencyLimiter) memoryUsage(model string) map[int]int {
	users := make(map[int]int)
	for id, lease := range limiter.memory[model] {
		if !lease.expires.IsZero() && !limiter.now().Before(lease.expires) {
			delete(limiter.memory[model], id)
		} else {
			users[lease.userID]++
		}
	}
	if len(limiter.memory[model]) == 0 {
		delete(limiter.memory, model)
	}
	return users
}

func (lease *ModelConcurrencyLease) Renew(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	limiter, identity := lease.limiter, lease.identity
	if limiter.redis != nil {
		operationCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		result, err := modelConcurrencyRenewScript.Run(operationCtx, limiter.redis, limiter.keys(identity.Model), strconv.Itoa(identity.UserID)+":"+identity.ID, identity.UserID, limiter.ttl.Milliseconds()).Int()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrModelConcurrencyUnavailable, err)
		}
		if result == 0 {
			return ErrModelConcurrencyLeaseExpired
		}
		return nil
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.memoryUsage(identity.Model)
	record, ok := limiter.memory[identity.Model][identity.ID]
	if !ok {
		return ErrModelConcurrencyLeaseExpired
	}
	if !record.expires.IsZero() {
		record.expires = limiter.now().Add(limiter.ttl)
	}
	limiter.memory[identity.Model][identity.ID] = record
	return nil
}

func (lease *ModelConcurrencyLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	limiter, identity := lease.limiter, lease.identity
	if limiter.redis != nil {
		operationCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := modelConcurrencyReleaseScript.Run(operationCtx, limiter.redis, limiter.keys(identity.Model), strconv.Itoa(identity.UserID)+":"+identity.ID, identity.UserID).Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrModelConcurrencyUnavailable, err)
		}
		return nil
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	delete(limiter.memory[identity.Model], identity.ID)
	limiter.memoryUsage(identity.Model)
	return nil
}

func (limiter *ModelConcurrencyLimiter) Usage(ctx context.Context, rule ModelConcurrencyRule) (ModelConcurrencyUsage, error) {
	users := make(map[int]int)
	if limiter.redis != nil {
		operationCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		values, err := modelConcurrencyUsageScript.Run(operationCtx, limiter.redis, limiter.keys(rule.Model)).StringSlice()
		if err != nil {
			return ModelConcurrencyUsage{}, fmt.Errorf("%w: %v", ErrModelConcurrencyUnavailable, err)
		}
		for i := 0; i+1 < len(values); i += 2 {
			uid, e1 := strconv.Atoi(values[i])
			count, e2 := strconv.Atoi(values[i+1])
			if e1 != nil || e2 != nil {
				return ModelConcurrencyUsage{}, ErrModelConcurrencyUnavailable
			}
			users[uid] = count
		}
	} else {
		limiter.mu.Lock()
		users = limiter.memoryUsage(rule.Model)
		limiter.mu.Unlock()
	}
	usage := ModelConcurrencyUsage{Model: rule.Model, Users: []ModelConcurrencyUserUsage{}}
	for uid, count := range users {
		usage.Total += count
		if !slices.ContainsFunc(rule.UserLimits, func(user ModelConcurrencyUserLimit) bool { return user.UserID == uid }) {
			usage.OtherUsers += count
		}
		usage.Users = append(usage.Users, ModelConcurrencyUserUsage{UserID: uid, Current: count})
	}
	sort.Slice(usage.Users, func(i, j int) bool { return usage.Users[i].UserID < usage.Users[j].UserID })
	return usage, nil
}

var modelConcurrencyTransferScript = redis.NewScript(modelConcurrencyRedisPrune + `
local old = redis.call('ZSCORE', KEYS[1], ARGV[1])
local existing = redis.call('ZSCORE', KEYS[1], ARGV[2])
if not old and not existing then return 0 end
if ARGV[1] ~= ARGV[2] and old then
  redis.call('ZREM', KEYS[1], ARGV[1])
  if existing then redis.call('HINCRBY', KEYS[2], ARGV[3], -1) end
end
redis.call('ZADD', KEYS[1], 9007199254740991, ARGV[2])
redis.call('PERSIST', KEYS[1])
redis.call('PERSIST', KEYS[2])
redis.call('SADD', KEYS[3], ARGV[4])
return 1
`)

// Transfer hands the same occupied slot to an already persisted asynchronous
// task. The caller must stop its HTTP heartbeat and must NOT defer Release
// after a successful transfer. Only terminal task state releases that slot.
func (lease *ModelConcurrencyLease) Transfer(ctx context.Context, taskID string) error {
	if lease == nil || !durableModelConcurrencyID.MatchString(taskID) {
		return errors.New("invalid durable concurrency task identity")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	limiter, identity := lease.limiter, lease.identity
	if limiter.redis != nil {
		operationCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		keys := append(limiter.keys(identity.Model), modelConcurrencyDurableModelsKey)
		oldMember := strconv.Itoa(identity.UserID) + ":" + identity.ID
		newMember := strconv.Itoa(identity.UserID) + ":" + taskID
		result, err := modelConcurrencyTransferScript.Run(operationCtx, limiter.redis, keys, oldMember, newMember, identity.UserID, identity.Model).Int()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrModelConcurrencyUnavailable, err)
		}
		if result == 0 {
			return ErrModelConcurrencyLeaseExpired
		}
	} else {
		limiter.mu.Lock()
		defer limiter.mu.Unlock()
		limiter.memoryUsage(identity.Model)
		record, ok := limiter.memory[identity.Model][identity.ID]
		if !ok {
			if _, exists := limiter.memory[identity.Model][taskID]; !exists {
				return ErrModelConcurrencyLeaseExpired
			}
		} else {
			delete(limiter.memory[identity.Model], identity.ID)
			record.expires = time.Time{}
			limiter.memory[identity.Model][taskID] = record
		}
	}
	lease.identity.ID, lease.identity.Persistent = taskID, true
	return nil
}

var modelConcurrencyRecoverScript = redis.NewScript(modelConcurrencyRedisPrune + `
if redis.call('ZADD', KEYS[1], 'NX', 9007199254740991, ARGV[1]) == 1 then
  redis.call('HINCRBY', KEYS[2], ARGV[2], 1)
end
redis.call('ZADD', KEYS[1], 'XX', 9007199254740991, ARGV[1])
redis.call('PERSIST', KEYS[1])
redis.call('PERSIST', KEYS[2])
redis.call('SADD', KEYS[3], ARGV[3])
return 1
`)

var modelConcurrencyListDurableScript = redis.NewScript(`
local members = redis.call('ZRANGEBYSCORE', KEYS[1], ARGV[2], '+inf')
if #members == 0 then redis.call('SREM', KEYS[2], ARGV[1]) end
return members
`)

// RecoverModelConcurrencyLease is ONLY for trusted, persisted, non-terminal
// tasks accepted earlier. It registers their existing work idempotently rather
// than subjecting a recovery to today's admission limits a second time.
func RecoverModelConcurrencyLease(ctx context.Context, identity ModelConcurrencyLeaseIdentity) (*ModelConcurrencyLease, error) {
	if !durableModelConcurrencyID.MatchString(identity.ID) {
		return nil, errors.New("only persisted tasks can recover concurrency leases")
	}
	if identity.Storage == "" {
		identity.Storage = ModelConcurrencyStorage()
	}
	identity.Persistent = true
	lease, err := RestoreModelConcurrencyLease(identity)
	if err != nil {
		return nil, err
	}
	limiter := lease.limiter
	if limiter.redis != nil {
		operationCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		keys := append(limiter.keys(identity.Model), modelConcurrencyDurableModelsKey)
		member := strconv.Itoa(identity.UserID) + ":" + identity.ID
		if err := modelConcurrencyRecoverScript.Run(operationCtx, limiter.redis, keys, member, identity.UserID, identity.Model).Err(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrModelConcurrencyUnavailable, err)
		}
	} else {
		limiter.mu.Lock()
		defer limiter.mu.Unlock()
		if limiter.memory[identity.Model] == nil {
			limiter.memory[identity.Model] = make(map[string]modelConcurrencyMemoryLease)
		}
		limiter.memory[identity.Model][identity.ID] = modelConcurrencyMemoryLease{userID: identity.UserID}
	}
	return lease, nil
}

// ListDurableModelConcurrencyLeases includes models removed from configuration
// or disabled while their tasks were running. Otherwise those slots would be
// orphaned and could block a later re-enable forever.
func ListDurableModelConcurrencyLeases(ctx context.Context) ([]ModelConcurrencyLeaseIdentity, error) {
	leases := []ModelConcurrencyLeaseIdentity{}
	if !RedisEnabled {
		modelConcurrencyMemory.mu.Lock()
		defer modelConcurrencyMemory.mu.Unlock()
		for model, active := range modelConcurrencyMemory.memory {
			modelConcurrencyMemory.memoryUsage(model)
			for id, record := range active {
				if record.expires.IsZero() {
					leases = append(leases, ModelConcurrencyLeaseIdentity{ID: id, Model: model, UserID: record.userID, Storage: "memory", Persistent: true})
				}
			}
		}
		return leases, nil
	}
	if RDB == nil {
		return nil, ErrModelConcurrencyUnavailable
	}
	operationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	models, err := RDB.SMembers(operationCtx, modelConcurrencyDurableModelsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelConcurrencyUnavailable, err)
	}
	limiter := NewModelConcurrencyLimiter(RDB, ModelConcurrencyLeaseTTL)
	for _, model := range models {
		members, err := modelConcurrencyListDurableScript.Run(operationCtx, RDB, []string{limiter.keys(model)[0], modelConcurrencyDurableModelsKey}, model, modelConcurrencyDurableScore).StringSlice()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrModelConcurrencyUnavailable, err)
		}
		for _, member := range members {
			user, id, ok := strings.Cut(member, ":")
			uid, err := strconv.Atoi(user)
			if !ok || err != nil || uid <= 0 || !durableModelConcurrencyID.MatchString(id) {
				return nil, ErrModelConcurrencyUnavailable
			}
			leases = append(leases, ModelConcurrencyLeaseIdentity{ID: id, Model: model, UserID: uid, Storage: "redis", Persistent: true})
		}
	}
	return leases, nil
}
