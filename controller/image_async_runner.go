package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type imageAsyncRunner struct {
	root, node                              string
	workers, queueLimit, userQueueLimit     int
	requestLimit, resultLimit, storageLimit int64
	timeout, retention                      time.Duration
	ctx                                     context.Context
	cancel                                  context.CancelFunc
	wg                                      sync.WaitGroup
	wake                                    chan struct{}
	admission                               sync.Mutex
	stopping                                atomic.Bool
}

var imageAsyncRuntime *imageAsyncRunner

func boundedImageAsyncEnv(name string, fallback, min, max int) int {
	n := common.GetEnvOrDefault(name, fallback)
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// StartImageAsyncRunner fails closed: an unwritable persistent store must never
// accept a task with 202. A stable node ID allows restarts to recover its jobs.
func StartImageAsyncRunner() error {
	if !strings.EqualFold(os.Getenv("IMAGE_ASYNC_ENABLED"), "true") {
		return nil
	}
	if common.BatchUpdateEnabled {
		return errors.New("asynchronous image billing requires BATCH_UPDATE_ENABLED=false")
	}
	root := os.Getenv("IMAGE_ASYNC_DIR")
	if root == "" {
		root = "/data/image-async"
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return err
	}
	nodePath := filepath.Join(root, "node-id")
	nodeBytes, err := os.ReadFile(nodePath)
	if errors.Is(err, os.ErrNotExist) {
		f, createErr := os.OpenFile(nodePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr == nil {
			_, err = f.WriteString(common.GetUUID())
			if err == nil {
				err = f.Sync()
			}
			_ = f.Close()
			if err != nil {
				return err
			}
			if err := syncImageTaskDir(root); err != nil {
				return err
			}
		} else if !errors.Is(createErr, os.ErrExist) {
			return createErr
		}
		nodeBytes, err = os.ReadFile(nodePath)
	}
	if err != nil {
		return err
	}
	if len(nodeBytes) < 16 || len(nodeBytes) > 128 {
		return errors.New("invalid image async storage node ID")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &imageAsyncRunner{root: root, node: string(nodeBytes), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		workers:        boundedImageAsyncEnv("IMAGE_ASYNC_WORKERS", 4, 1, 32),
		queueLimit:     boundedImageAsyncEnv("IMAGE_ASYNC_QUEUE_LIMIT", 100, 1, 10000),
		userQueueLimit: boundedImageAsyncEnv("IMAGE_ASYNC_USER_QUEUE_LIMIT", 20, 1, 1000),
		requestLimit:   int64(boundedImageAsyncEnv("IMAGE_ASYNC_REQUEST_MB", 64, 1, 256)) << 20,
		resultLimit:    int64(boundedImageAsyncEnv("IMAGE_ASYNC_RESULT_MB", 256, 1, 1024)) << 20,
		storageLimit:   int64(boundedImageAsyncEnv("IMAGE_ASYNC_STORAGE_MB", 10240, 256, 1048576)) << 20,
		timeout:        time.Duration(boundedImageAsyncEnv("IMAGE_ASYNC_TIMEOUT_SECONDS", 600, 30, 3600)) * time.Second,
		retention:      time.Duration(boundedImageAsyncEnv("IMAGE_ASYNC_RETENTION_HOURS", 24, 2, 168)) * time.Hour,
	}
	imageAsyncRuntime = r
	model.EnableImageAsyncQuotaReads()
	for i := 0; i < r.workers; i++ {
		r.wg.Add(1)
		go r.worker()
	}
	r.wg.Add(1)
	go r.maintenance()
	common.SysLog(fmt.Sprintf("asynchronous image processing enabled: workers=%d", r.workers))
	return nil
}

func StopImageAsyncRunner(ctx context.Context) {
	r := imageAsyncRuntime
	if r == nil {
		return
	}
	r.stopping.Store(true)
	r.cancel()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (r *imageAsyncRunner) worker() {
	defer r.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if r.ctx.Err() != nil {
			return
		}
		owner := common.GetUUID()
		task, err := model.ClaimImageAsyncTask(r.node, owner, 90)
		if err != nil {
			common.SysError("image async claim failed: " + err.Error())
		}
		if task != nil {
			r.execute(task, owner)
			continue
		}
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
	}
}

func (r *imageAsyncRunner) execute(task *model.ImageAsyncTask, owner string) {
	status, code, message := 500, "image_task_execution_failed", "Image task failed; use request_id when contacting support"
	defer func() {
		if recovered := recover(); recovered != nil {
			common.SysError(fmt.Sprintf("image task %s panicked", task.ID))
		}
		if status != 200 {
			current, err := model.GetImageAsyncTask(task.ID, task.UserID, task.TokenID)
			info, fileErr := os.Stat(filepath.Join(imageTaskDir(r.root, task.ID), "result.json"))
			if err == nil && current != nil && current.BillingState == model.ImageAsyncBillingSettled && fileErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
				status, code, message = 200, "", ""
			}
		}
		if status != 200 {
			if _, err := model.RefundImageAsyncQuota(task.ID, owner); err != nil && !errors.Is(err, model.ErrImageAsyncBillingFinalized) {
				common.SysError("image async refund pending for " + task.ID + ": " + err.Error())
			}
		}
		if err := model.CompleteImageAsyncTask(task.ID, owner, status, code, message); err != nil {
			common.SysError("image async completion pending for " + task.ID + ": " + err.Error())
		}
		// Queued requests are never replayed after execution starts. Input is no
		// longer needed even if recovery must finish a pending refund.
		_ = os.Remove(filepath.Join(imageTaskDir(r.root, task.ID), "request"))
	}()
	ctx, cancel := context.WithTimeout(r.ctx, r.timeout)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := model.RenewImageAsyncTaskLease(task.ID, owner, 90); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	token, err := model.GetTokenByIds(task.TokenID, task.UserID)
	if err != nil {
		status, code, message = 401, "image_task_token_unavailable", "Task token is no longer available"
		return
	}
	input, err := os.Open(filepath.Join(imageTaskDir(r.root, task.ID), "request"))
	if err != nil {
		code, message = "image_task_input_unavailable", "Stored task input is unavailable"
		return
	}
	defer input.Close()
	inputInfo, err := input.Stat()
	if err != nil {
		return
	}
	r.admission.Lock()
	usage, usageErr := imageTaskStorageUsage(r.root)
	running, countErr := model.ImageAsyncRunningCount(r.node)
	storageAvailable := usageErr == nil && countErr == nil && usage <= r.storageLimit-running*r.resultLimit
	r.admission.Unlock()
	if !storageAvailable {
		status, code, message = 503, "image_task_storage_full", "Image task storage is full; no generation was started"
		return
	}
	writer, err := newImageTaskResponseWriter(imageTaskDir(r.root, task.ID), r.resultLimit)
	if err != nil {
		code, message = "image_task_storage_error", "Unable to store generated image"
		return
	}
	defer writer.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, task.Operation, input)
	if err != nil {
		return
	}
	request.ContentLength = inputInfo.Size()
	request.RemoteAddr = net.JoinHostPort(task.ClientIP, "0")
	request.Header.Set("Content-Type", task.ContentType)
	request.Header.Set("Accept", "application/json")
	key := "Bearer sk-" + token.Key
	if task.SpecificChannelID != "" {
		key += "-" + task.SpecificChannelID
	}
	request.Header.Set("Authorization", key)
	var headers map[string]string
	if err := common.UnmarshalJsonStr(task.HeadersJSON, &headers); err != nil {
		return
	}
	for _, name := range []string{"X-Size-Fit", "User-Agent"} {
		if value := headers[name]; value != "" {
			request.Header.Set(name, value)
		}
	}
	engine := gin.New()
	_ = engine.SetTrustedProxies(nil)
	engine.Use(middleware.BodyStorageCleanup())
	engine.Use(func(c *gin.Context) {
		c.Set(common.RequestIdKey, task.RequestID)
		c.Set(string(constant.ContextKeyRequestStartTime), time.Now())
		c.Set(service.ImageAsyncTaskIDContextKey, task.ID)
		c.Set(service.ImageAsyncOwnerContextKey, owner)
		c.Set(service.ImageAsyncResultCommitContextKey, writer.Commit)
		c.Next()
		if value, ok := c.Get("image_async_relay_error"); ok {
			if apiErr, ok := value.(*types.NewAPIError); ok {
				status = apiErr.StatusCode
				if status < 400 {
					status = 502
				}
				code = string(apiErr.GetErrorCode())
				// Do not persist arbitrary provider text: it can contain credentials.
				message = "Image generation failed; use request_id when contacting support"
			}
		}
	})
	engine.Use(middleware.TokenAuth())
	engine.Use(func(c *gin.Context) {
		if common.GetContextKeyString(c, constant.ContextKeyTokenGroup) != task.TokenGroup || common.GetContextKeyString(c, constant.ContextKeyUserGroup) != task.UserGroup {
			imageAsyncError(c, 409, "image_task_group_changed", "Account or token group changed while the task was queued")
			return
		}
		c.Next()
	})
	engine.Use(middleware.Distribute())
	engine.POST(task.Operation, func(c *gin.Context) { Relay(c, types.RelayFormatOpenAIImage) })
	engine.ServeHTTP(writer, request)
	current, err := model.GetImageAsyncTask(task.ID, task.UserID, task.TokenID)
	if err != nil || current == nil {
		return
	}
	if writer.committed && current.BillingState == model.ImageAsyncBillingSettled {
		status, code, message = 200, "", ""
		return
	}
	if ctx.Err() != nil {
		status, code, message = 504, "image_task_timeout", "Image task did not complete before its execution deadline"
		return
	}
	if writer.status >= 400 && code == "image_task_execution_failed" {
		status = writer.status
		code = "image_task_request_rejected"
		message = "Image task was rejected; check current token permissions, balance, model and group"
	}
}

func (r *imageAsyncRunner) maintenance() {
	defer r.wg.Done()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		r.recoverAndClean()
		select {
		case <-r.ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (r *imageAsyncRunner) recoverAndClean() {
	if _, err := model.ExpireQueuedImageAsyncTasks(r.node); err != nil {
		common.SysError("image async queue expiry failed: " + err.Error())
	}
	tasks, err := model.ListExpiredRunningImageTasks(r.node)
	if err != nil {
		common.SysError("image async recovery lookup failed: " + err.Error())
		return
	}
	for _, task := range tasks {
		info, fileErr := os.Stat(filepath.Join(imageTaskDir(r.root, task.ID), "result.json"))
		success := task.BillingState == model.ImageAsyncBillingSettled && fileErr == nil && info.Mode().IsRegular() && info.Size() > 0
		if _, err := model.RecoverExpiredImageAsyncTask(task.ID, r.node, success, "image_task_interrupted", "Task execution was interrupted; no automatic regeneration was attempted"); err != nil {
			common.SysError("image async recovery pending for " + task.ID + ": " + err.Error())
		}
	}
	pending, err := model.ListImageAsyncRefundPending(r.node, 100)
	if err == nil {
		for _, task := range pending {
			if _, err := model.RefundImageAsyncQuota(task.ID, ""); err != nil {
				common.SysError("image async refund retry failed for " + task.ID)
			}
		}
	}
	expired, err := model.ListExpiredImageAsyncTasks(r.node, 100)
	if err != nil {
		return
	}
	for _, task := range expired {
		if err := os.RemoveAll(imageTaskDir(r.root, task.ID)); err != nil {
			continue
		}
		if err := model.DeleteExpiredImageAsyncTask(task.ID, r.node); err != nil {
			common.SysError("image async expiry cleanup failed for " + task.ID)
		}
	}
	// A crash between fsync(input) and the admission transaction can leave an
	// orphan. Retain it for a full TTL, and never infer absence from a DB error.
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return
	}
	cleaned := 0
	for _, entry := range entries {
		if cleaned >= 100 {
			break
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "imgtask_") {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < r.retention {
			continue
		}
		exists, err := model.ImageAsyncTaskExists(entry.Name())
		if err != nil {
			return
		}
		if !exists {
			_ = os.RemoveAll(imageTaskDir(r.root, entry.Name()))
			cleaned++
		}
	}
}
