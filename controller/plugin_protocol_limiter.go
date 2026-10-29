package controller

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

var (
	errPluginProtocolObservationLimitExceeded   = errors.New("plugin protocol observation limit exceeded")
	errInvalidPluginProtocolObservationIdentity = errors.New("invalid plugin protocol observation identity")
)

type pluginProtocolObservationLimits struct {
	global    int
	perPlugin int
	perUser   int
	perToken  int
}

// A task observation holds a slot only while its submission is in flight,
// so these caps bound concurrent submissions, not running tasks. Clients
// that fan out a batch (hundreds of background image requests on one token)
// must fit under the per-token cap or they receive an immediate 429.
var defaultPluginProtocolObservationLimits = pluginProtocolObservationLimits{
	global:    2048,
	perPlugin: 1024,
	perUser:   256,
	perToken:  256,
}

var pluginProtocolObservationAdmissions = newPluginProtocolObservationLimiter(
	pluginProtocolObservationLimitsFromEnv(),
)

// pluginProtocolObservationLimitsFromEnv applies the TASK_OBSERVATION_LIMIT_*
// overrides; a missing, malformed or non-positive value keeps the default for
// that scope so a bad deploy cannot close the endpoint entirely.
func pluginProtocolObservationLimitsFromEnv() pluginProtocolObservationLimits {
	limits := defaultPluginProtocolObservationLimits
	for _, scope := range []struct {
		env   string
		value *int
	}{
		{env: "TASK_OBSERVATION_LIMIT_GLOBAL", value: &limits.global},
		{env: "TASK_OBSERVATION_LIMIT_PLUGIN", value: &limits.perPlugin},
		{env: "TASK_OBSERVATION_LIMIT_USER", value: &limits.perUser},
		{env: "TASK_OBSERVATION_LIMIT_TOKEN", value: &limits.perToken},
	} {
		if configured := common.GetEnvOrDefault(scope.env, *scope.value); configured > 0 {
			*scope.value = configured
		}
	}
	return limits
}

type pluginProtocolObservationLimitError struct {
	scope string
	limit int
}

func (e *pluginProtocolObservationLimitError) Error() string {
	return fmt.Sprintf("%s: %s capacity is %d", errPluginProtocolObservationLimitExceeded, e.scope, e.limit)
}

func (e *pluginProtocolObservationLimitError) Unwrap() error {
	return errPluginProtocolObservationLimitExceeded
}

type pluginProtocolObservationLimiter struct {
	mu sync.Mutex

	limits pluginProtocolObservationLimits
	global int
	plugin map[string]int
	user   map[int]int
	token  map[int]int
}

func newPluginProtocolObservationLimiter(limits pluginProtocolObservationLimits) *pluginProtocolObservationLimiter {
	return &pluginProtocolObservationLimiter{
		limits: limits,
		plugin: make(map[string]int),
		user:   make(map[int]int),
		token:  make(map[int]int),
	}
}

func (l *pluginProtocolObservationLimiter) acquire(
	pluginKey string,
	userID int,
	tokenID int,
) (func(), error) {
	pluginKey = strings.TrimSpace(pluginKey)
	switch {
	case pluginKey == "":
		return nil, fmt.Errorf("%w: plugin key is required", errInvalidPluginProtocolObservationIdentity)
	case userID <= 0:
		return nil, fmt.Errorf("%w: user id must be positive", errInvalidPluginProtocolObservationIdentity)
	case tokenID <= 0:
		return nil, fmt.Errorf("%w: token id must be positive", errInvalidPluginProtocolObservationIdentity)
	}

	l.mu.Lock()
	if l.global >= l.limits.global {
		l.mu.Unlock()
		return nil, &pluginProtocolObservationLimitError{
			scope: "global",
			limit: l.limits.global,
		}
	}
	l.global++

	if l.plugin[pluginKey] >= l.limits.perPlugin {
		l.global--
		l.mu.Unlock()
		return nil, &pluginProtocolObservationLimitError{
			scope: "plugin",
			limit: l.limits.perPlugin,
		}
	}
	l.plugin[pluginKey]++

	if l.user[userID] >= l.limits.perUser {
		l.global--
		l.plugin[pluginKey]--
		if l.plugin[pluginKey] == 0 {
			delete(l.plugin, pluginKey)
		}
		l.mu.Unlock()
		return nil, &pluginProtocolObservationLimitError{
			scope: "user",
			limit: l.limits.perUser,
		}
	}
	l.user[userID]++

	if l.token[tokenID] >= l.limits.perToken {
		l.global--
		l.plugin[pluginKey]--
		if l.plugin[pluginKey] == 0 {
			delete(l.plugin, pluginKey)
		}
		l.user[userID]--
		if l.user[userID] == 0 {
			delete(l.user, userID)
		}
		l.mu.Unlock()
		return nil, &pluginProtocolObservationLimitError{
			scope: "token",
			limit: l.limits.perToken,
		}
	}
	l.token[tokenID]++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()

			l.global--

			l.plugin[pluginKey]--
			if l.plugin[pluginKey] == 0 {
				delete(l.plugin, pluginKey)
			}

			l.user[userID]--
			if l.user[userID] == 0 {
				delete(l.user, userID)
			}

			l.token[tokenID]--
			if l.token[tokenID] == 0 {
				delete(l.token, tokenID)
			}
		})
	}, nil
}
