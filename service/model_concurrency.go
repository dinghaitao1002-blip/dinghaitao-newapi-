package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

const modelConcurrencyRequestKey = "model_concurrency_request"

type modelConcurrencyRequest struct {
	mu              sync.Mutex
	lease           *common.ModelConcurrencyLease
	durable         bool
	actualGroup     string
	lostLease       context.Context
	cancelLostLease context.CancelFunc
}

func (request *modelConcurrencyRequest) renew(ctx context.Context) error {
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.durable || request.lease == nil {
		return nil
	}
	if err := request.lease.Renew(ctx); err != nil {
		request.cancelLostLease()
		return err
	}
	return nil
}

var modelConcurrencyRecovery struct {
	sync.Mutex
	configuration string
	ready         bool
}

// ReconcileModelConcurrency recovers accepted tasks after a restart and reclaims
// slots for tasks deleted or finalized through paths other than normal polling.
func ReconcileModelConcurrency(ctx context.Context) error {
	modelConcurrencyRecovery.Lock()
	defer modelConcurrencyRecovery.Unlock()
	config, generation := setting.GetModelConcurrencyConfigSnapshot()
	if err := model.ReconcileModelConcurrencyTasks(ctx, config); err != nil {
		modelConcurrencyRecovery.ready = false
		return err
	}
	data, _ := common.Marshal(config)
	modelConcurrencyRecovery.configuration = common.ModelConcurrencyStorage() + ":" + strconv.FormatUint(generation, 10) + ":" + string(data)
	modelConcurrencyRecovery.ready = true
	return nil
}

// StartModelConcurrencyRecovery must run after database/options initialization,
// before serving requests. Admission also recovers synchronously after rule edits.
func StartModelConcurrencyRecovery(ctx context.Context) error {
	initialErr := ReconcileModelConcurrency(ctx)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := ReconcileModelConcurrency(ctx); err != nil {
					common.SysError("model concurrency recovery failed: " + err.Error())
				}
			}
		}
	}()
	return initialErr
}

// BeginModelConcurrency occupies one slot for the entire request, including
// retries and streaming. Re-entry by a task/protocol handler never acquires twice.
func BeginModelConcurrency(c *gin.Context, modelName string) (func(), error) {
	if _, exists := c.Get(modelConcurrencyRequestKey); exists {
		return func() {}, nil
	}
	config, generation := setting.GetModelConcurrencyConfigSnapshot()
	if !config.HasEnabledRules() {
		return func() {}, nil
	}
	actualGroup := EffectiveModelConcurrencyGroup(c)
	initialGroup := actualGroup
	durable := IsImageAsyncRequest(c)
	if durable {
		var err error
		initialGroup, err = model.ImageConcurrencyGroup(c.Request.Context(), c.GetString(ImageAsyncTaskIDContextKey), c.GetInt("id"))
		if err != nil {
			invalidateModelConcurrencyRecovery(err)
			return nil, err
		}
	}
	var lease *common.ModelConcurrencyLease
	if rule, enabled := setting.ResolveModelConcurrencyRule(config, modelName, initialGroup); enabled {
		if err := ensureModelConcurrencyRecovery(c.Request.Context(), config, generation); err != nil {
			return nil, err
		}
		var err error
		if durable {
			lease, err = common.RecoverModelConcurrencyLease(c.Request.Context(), common.ModelConcurrencyLeaseIdentity{
				ID: "image:" + c.GetString(ImageAsyncTaskIDContextKey), Model: rule.Model, Group: rule.Group, UserID: c.GetInt("id"),
			})
		} else {
			lease, err = common.AcquireModelConcurrency(c.Request.Context(), rule.Model, c.GetInt("id"), rule)
		}
		if err != nil {
			invalidateModelConcurrencyRecovery(err)
			return nil, err
		}
	}
	lostLease, cancelLostLease := context.WithCancel(context.Background())
	state := &modelConcurrencyRequest{lease: lease, durable: durable, actualGroup: initialGroup, lostLease: lostLease, cancelLostLease: cancelLostLease}
	c.Set(modelConcurrencyRequestKey, state)
	if err := RebindModelConcurrency(c, modelName); err != nil {
		cancelLostLease()
		if lease != nil && !durable {
			_ = lease.Release(context.Background())
		}
		return nil, err
	}
	requestContext, cancelRequest := context.WithCancel(c.Request.Context())
	c.Request = c.Request.Clone(requestContext)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(common.ModelConcurrencyHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := state.renew(ctx)
				if err != nil {
					common.SysError("model concurrency heartbeat failed: " + err.Error())
					modelConcurrencyRecovery.Lock()
					modelConcurrencyRecovery.ready = false
					modelConcurrencyRecovery.Unlock()
					cancelRequest()
					return
				}
			}
		}
	}()
	return func() {
		cancelLostLease()
		cancelRequest()
		cancel()
		<-done
		state.mu.Lock()
		defer state.mu.Unlock()
		if !state.durable && state.lease != nil {
			if err := state.lease.Release(context.Background()); err != nil {
				common.SysError("model concurrency release failed: " + err.Error())
			}
		}
	}, nil
}

// DetachModelConcurrencyContext preserves accepted plugin submission semantics:
// a client disconnect cannot cancel it, but loss of concurrency ownership can.
// Every detached hook/submission must retain this independent cancellation path.
func DetachModelConcurrencyContext(c *gin.Context, parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	value, exists := c.Get(modelConcurrencyRequestKey)
	if !exists {
		return ctx, cancel
	}
	state := value.(*modelConcurrencyRequest)
	stop := context.AfterFunc(state.lostLease, cancel)
	return ctx, func() { stop(); cancel() }
}

// TransferModelConcurrency retains the already occupied slot after an accepted
// asynchronous submission. Never release accepted work just because HTTP ends.
func TransferModelConcurrency(c *gin.Context, taskID string) error {
	value, exists := c.Get(modelConcurrencyRequestKey)
	if !exists {
		return nil
	}
	state := value.(*modelConcurrencyRequest)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.durable || state.lease == nil {
		return nil
	}
	if err := state.lease.Transfer(context.Background(), taskID); err != nil {
		state.durable = true
		identity := state.lease.Identity()
		identity.ID, identity.Persistent = taskID, true
		recovered, recoverErr := common.RecoverModelConcurrencyLease(context.Background(), identity)
		if recoverErr != nil {
			// The accepted DB row is authoritative. Require recovery before any
			// further admission instead of waiting for the periodic sweep.
			modelConcurrencyRecovery.Lock()
			modelConcurrencyRecovery.ready = false
			modelConcurrencyRecovery.Unlock()
			return errors.Join(err, recoverErr)
		}
		if err := state.lease.Release(context.Background()); err != nil {
			common.SysError("provisional model concurrency release failed: " + err.Error())
		}
		state.lease = recovered
	}
	state.durable = true
	return model.ReleaseFinishedModelConcurrencyTask(context.Background(), state.lease.Identity())
}

// EffectiveModelConcurrencyGroup follows actual channel routing, including
// auto-group selection, rather than placing all auto tokens in one bucket.
func EffectiveModelConcurrencyGroup(c *gin.Context) string {
	if selected := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selected != "" {
		return selected
	}
	return common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
}

func ensureModelConcurrencyRecovery(ctx context.Context, config common.ModelConcurrencyConfig, generation uint64) error {
	if !config.HasEnabledRules() {
		return nil
	}
	data, _ := common.Marshal(config)
	configuration := common.ModelConcurrencyStorage() + ":" + strconv.FormatUint(generation, 10) + ":" + string(data)
	modelConcurrencyRecovery.Lock()
	defer modelConcurrencyRecovery.Unlock()
	if !modelConcurrencyRecovery.ready || modelConcurrencyRecovery.configuration != configuration {
		if err := model.ReconcileModelConcurrencyTasks(ctx, config); err != nil {
			modelConcurrencyRecovery.ready = false
			return err
		}
		modelConcurrencyRecovery.configuration, modelConcurrencyRecovery.ready = configuration, true
	}
	return nil
}

func invalidateModelConcurrencyRecovery(err error) {
	if err != nil && !errors.Is(err, common.ErrModelConcurrencyExceeded) {
		modelConcurrencyRecovery.Lock()
		modelConcurrencyRecovery.ready = false
		modelConcurrencyRecovery.Unlock()
	}
}

// RebindModelConcurrency runs after every channel selection. Reserve the
// destination before releasing the old slot so cross-group retries cannot
// bypass admission. The original request's finish function owns the new lease.
func RebindModelConcurrency(c *gin.Context, modelName string) error {
	value, exists := c.Get(modelConcurrencyRequestKey)
	if !exists {
		// Even while limits are disabled, durable workers must retain their
		// actual routed group so a later enable/restart counts them correctly.
		if IsImageAsyncRequest(c) && c.GetString(ImageAsyncOwnerContextKey) != "" && !setting.GetModelConcurrencyConfig().HasEnabledRules() {
			return model.MoveImageConcurrencyGroup(c.Request.Context(), c.GetString(ImageAsyncTaskIDContextKey), c.GetString(ImageAsyncOwnerContextKey), EffectiveModelConcurrencyGroup(c))
		}
		return nil
	}
	state := value.(*modelConcurrencyRequest)
	state.mu.Lock()
	defer state.mu.Unlock()
	group := EffectiveModelConcurrencyGroup(c)
	// Editing rules affects new admissions. Requests already admitted to this
	// group retain their original slot until completion; only a real routing
	// move must reserve a destination scope.
	if state.actualGroup == group {
		return nil
	}
	config, generation := setting.GetModelConcurrencyConfigSnapshot()
	rule, enabled := setting.ResolveModelConcurrencyRule(config, modelName, group)
	sameBucket := state.lease == nil && !enabled
	if state.lease != nil && enabled {
		identity := state.lease.Identity()
		sameBucket = identity.Group == rule.Group && identity.Model == rule.Model
	}
	var next *common.ModelConcurrencyLease
	if !sameBucket && enabled {
		if err := ensureModelConcurrencyRecovery(c.Request.Context(), config, generation); err != nil {
			return err
		}
		var err error
		next, err = common.AcquireModelConcurrency(c.Request.Context(), rule.Model, c.GetInt("id"), rule)
		if err != nil {
			invalidateModelConcurrencyRecovery(err)
			return err
		}
	}
	if state.durable && state.actualGroup != group {
		if err := model.MoveImageConcurrencyGroup(c.Request.Context(), c.GetString(ImageAsyncTaskIDContextKey), c.GetString(ImageAsyncOwnerContextKey), group); err != nil {
			if next != nil {
				_ = next.Release(context.Background())
			}
			return err
		}
	}
	if sameBucket {
		state.actualGroup = group
		return nil
	}
	if state.durable && next != nil {
		if err := next.Transfer(c.Request.Context(), "image:"+c.GetString(ImageAsyncTaskIDContextKey)); err != nil {
			// The destination group is already persisted. Recovery must reconstruct
			// its accepted slot before any subsequent request can enter.
			modelConcurrencyRecovery.Lock()
			modelConcurrencyRecovery.ready = false
			modelConcurrencyRecovery.Unlock()
			_ = next.Release(context.Background())
			return err
		}
	}
	if state.lease != nil {
		if err := state.lease.Release(context.Background()); err != nil {
			invalidateModelConcurrencyRecovery(err)
			if next != nil && !state.durable {
				_ = next.Release(context.Background())
			}
			state.cancelLostLease()
			return err
		}
	}
	state.lease, state.actualGroup = next, group
	if state.durable && next != nil {
		return model.ReleaseFinishedModelConcurrencyTask(c.Request.Context(), next.Identity())
	}
	return nil
}
