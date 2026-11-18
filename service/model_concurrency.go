package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

const modelConcurrencyRequestKey = "model_concurrency_request"

type modelConcurrencyRequest struct {
	mu              sync.Mutex
	lease           *common.ModelConcurrencyLease
	durable         bool
	lostLease       context.Context
	cancelLostLease context.CancelFunc
}

func (request *modelConcurrencyRequest) renew(ctx context.Context) error {
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.durable {
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
	config := setting.GetModelConcurrencyConfig()
	if err := model.ReconcileModelConcurrencyTasks(ctx, config); err != nil {
		modelConcurrencyRecovery.ready = false
		return err
	}
	data, _ := common.Marshal(config)
	modelConcurrencyRecovery.configuration = common.ModelConcurrencyStorage() + string(data)
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
	config := setting.GetModelConcurrencyConfig()
	rule, enabled := setting.ResolveModelConcurrencyRule(config, modelName)
	if !enabled {
		return func() {}, nil
	}
	modelName = rule.Model
	data, _ := common.Marshal(config)
	configuration := common.ModelConcurrencyStorage() + string(data)
	modelConcurrencyRecovery.Lock()
	if !modelConcurrencyRecovery.ready || modelConcurrencyRecovery.configuration != configuration {
		if err := model.ReconcileModelConcurrencyTasks(c.Request.Context(), config); err != nil {
			modelConcurrencyRecovery.Unlock()
			return nil, err
		}
		modelConcurrencyRecovery.configuration, modelConcurrencyRecovery.ready = configuration, true
	}
	modelConcurrencyRecovery.Unlock()
	var lease *common.ModelConcurrencyLease
	var err error
	durable := IsImageAsyncRequest(c)
	if durable {
		lease, err = common.RecoverModelConcurrencyLease(c.Request.Context(), common.ModelConcurrencyLeaseIdentity{
			ID: "image:" + c.GetString(ImageAsyncTaskIDContextKey), Model: modelName, UserID: c.GetInt("id"),
		})
	} else {
		lease, err = common.AcquireModelConcurrency(c.Request.Context(), modelName, c.GetInt("id"), rule)
	}
	if err != nil {
		if !errors.Is(err, common.ErrModelConcurrencyExceeded) {
			modelConcurrencyRecovery.Lock()
			modelConcurrencyRecovery.ready = false
			modelConcurrencyRecovery.Unlock()
		}
		return nil, err
	}
	lostLease, cancelLostLease := context.WithCancel(context.Background())
	state := &modelConcurrencyRequest{lease: lease, durable: durable, lostLease: lostLease, cancelLostLease: cancelLostLease}
	c.Set(modelConcurrencyRequestKey, state)
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
		if !state.durable {
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
	if state.durable {
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
