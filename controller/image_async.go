package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// Async is opt-in at the gateway; no control field is forwarded to providers.
func RelayImage(c *gin.Context) {
	async := c.Query("async") == "true"
	for _, pref := range strings.Split(c.GetHeader("Prefer"), ",") {
		if strings.EqualFold(strings.TrimSpace(pref), "respond-async") {
			async = true
		}
	}
	if !async {
		Relay(c, types.RelayFormatOpenAIImage)
		return
	}
	submitImageAsyncTask(c)
}

func imageAsyncError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "image_task_error", "code": code, "message": message}})
}

func submitImageAsyncTask(c *gin.Context) {
	r := imageAsyncRuntime
	if r == nil || r.stopping.Load() {
		imageAsyncError(c, 503, "image_async_unavailable", "Asynchronous image processing is unavailable")
		return
	}
	request, err := helper.GetAndValidateRequest(c, types.RelayFormatOpenAIImage)
	if err != nil {
		imageAsyncError(c, 400, "invalid_request", err.Error())
		return
	}
	if imageRequest, ok := request.(*dto.ImageRequest); ok && imageRequest.Stream != nil && *imageRequest.Stream {
		imageAsyncError(c, 400, "async_stream_not_supported", "Use stream=false for asynchronous image requests")
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(key) > 128 {
		imageAsyncError(c, 400, "invalid_idempotency_key", "Idempotency-Key must not exceed 128 bytes")
		return
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		imageAsyncError(c, 400, "invalid_request", "Unable to read image request")
		return
	}
	if storage.Size() > r.requestLimit {
		imageAsyncError(c, 413, "request_too_large", "Async image request exceeds the upload limit")
		return
	}
	headers := map[string]string{}
	for _, name := range []string{"X-Size-Fit", "User-Agent"} {
		if value := c.GetHeader(name); value != "" {
			if len(value) > 1024 {
				imageAsyncError(c, 400, "invalid_header", "Image request header is too long")
				return
			}
			headers[name] = value
		}
	}
	headerBytes, err := common.Marshal(headers)
	if err != nil {
		imageAsyncError(c, 500, "image_task_storage_error", "Unable to prepare task")
		return
	}
	tokenGroup := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
	userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	specific := common.GetContextKeyString(c, constant.ContextKeyTokenSpecificChannelId)
	fingerprintMeta, _ := common.Marshal([]string{string(headerBytes), tokenGroup, userGroup, specific})
	reader, err := storage.NewReader()
	if err != nil {
		imageAsyncError(c, 500, "image_task_storage_error", "Unable to read task input")
		return
	}
	fingerprint, err := imageTaskFingerprint(reader, c.GetHeader("Content-Type"), c.Request.URL.Path, string(fingerprintMeta))
	reader.Close()
	if err != nil {
		imageAsyncError(c, 400, "invalid_request", "Unable to parse image task input")
		return
	}
	now := time.Now().Unix()
	task := &model.ImageAsyncTask{
		ID: "imgtask_" + common.GetUUID(), UserID: c.GetInt("id"), TokenID: c.GetInt("token_id"),
		Status: "queued", Operation: c.Request.URL.Path, ContentType: c.GetHeader("Content-Type"),
		HeadersJSON: string(headerBytes), ClientIP: c.ClientIP(), TokenGroup: tokenGroup, UserGroup: userGroup,
		UsingGroup: common.GetContextKeyString(c, constant.ContextKeyUsingGroup), SpecificChannelID: specific,
		Model: common.GetContextKeyString(c, constant.ContextKeyOriginalModel), RequestID: c.GetString(common.RequestIdKey),
		Fingerprint: fingerprint, StorageNode: r.node, CreatedAt: now, ExpiresAt: now + int64(r.retention/time.Second),
	}
	if task.RequestID == "" {
		task.RequestID = common.NewRequestId()
	}
	if key != "" {
		h := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", task.UserID, task.TokenID, key)))
		value := hex.EncodeToString(h[:])
		task.IdempotencyHash = &value
	}
	// Serialize local admission against the disk budget. The DB additionally
	// enforces queue limits and idempotency across processes.
	r.admission.Lock()
	defer r.admission.Unlock()
	usage, err := imageTaskStorageUsage(r.root)
	if err != nil || usage+storage.Size()+int64(r.workers)*r.resultLimit > r.storageLimit {
		imageAsyncError(c, 503, "image_task_storage_full", "Image task storage is full; retry later")
		return
	}
	dir := imageTaskDir(r.root, task.ID)
	reader, err = storage.NewReader()
	if err != nil {
		imageAsyncError(c, 500, "image_task_storage_error", "Unable to persist task input")
		return
	}
	err = writeImageTaskRequest(dir, reader, r.requestLimit)
	reader.Close()
	if err != nil {
		_ = os.RemoveAll(dir)
		imageAsyncError(c, 503, "image_task_storage_error", "Unable to persist task input")
		return
	}
	stored, created, err := model.CreateImageAsyncTask(task, r.queueLimit, r.userQueueLimit)
	if !created && (err == nil || errors.Is(err, model.ErrImageAsyncConflict) || errors.Is(err, model.ErrImageAsyncCapacity)) {
		_ = os.RemoveAll(dir)
	}
	if err != nil {
		switch {
		case errors.Is(err, model.ErrImageAsyncConflict):
			imageAsyncError(c, 409, "idempotency_conflict", "Idempotency-Key was already used with a different request")
		case errors.Is(err, model.ErrImageAsyncCapacity):
			c.Header("Retry-After", "10")
			imageAsyncError(c, 429, "image_task_queue_full", "Image task queue is full; retry later")
		default:
			imageAsyncError(c, 503, "image_task_storage_error", "Unable to save image task")
		}
		return
	}
	c.Header("Preference-Applied", "respond-async")
	c.Header("Location", "/v1/images/tasks/"+stored.ID)
	c.Header("Retry-After", "3")
	c.JSON(http.StatusAccepted, imageAsyncTaskResponse(stored))
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func imageAsyncTaskResponse(task *model.ImageAsyncTask) gin.H {
	result := gin.H{"id": task.ID, "task_id": task.ID, "object": "image.task", "status": task.Status,
		"created_at": task.CreatedAt, "started_at": task.StartedAt, "finished_at": task.FinishedAt,
		"expires_at": task.ExpiresAt, "request_id": task.RequestID, "model": task.Model,
		"status_url": "/v1/images/tasks/" + task.ID, "result_url": "/v1/images/tasks/" + task.ID + "/result"}
	if task.Status == "failed" {
		result["error"] = gin.H{"code": task.ErrorCode, "message": task.ErrorMessage, "status_code": task.HTTPStatus}
	}
	return result
}

func getOwnedImageAsyncTask(c *gin.Context) *model.ImageAsyncTask {
	c.Header("Cache-Control", "no-store")
	task, err := model.GetImageAsyncTask(c.Param("task_id"), c.GetInt("id"), c.GetInt("token_id"))
	if err != nil {
		imageAsyncError(c, 503, "image_task_lookup_failed", "Unable to look up image task")
		return nil
	}
	if task == nil {
		imageAsyncError(c, 404, "image_task_not_found", "Image task was not found")
		return nil
	}
	if task.ExpiresAt <= time.Now().Unix() {
		imageAsyncError(c, 410, "image_task_expired", "Image task and result have expired")
		return nil
	}
	return task
}

func GetImageAsyncTask(c *gin.Context) {
	task := getOwnedImageAsyncTask(c)
	if task == nil {
		return
	}
	c.Header("Retry-After", "3")
	c.JSON(200, imageAsyncTaskResponse(task))
}

func GetImageAsyncTaskResult(c *gin.Context) {
	task := getOwnedImageAsyncTask(c)
	if task == nil {
		return
	}
	if task.Status == "queued" || task.Status == "running" {
		c.Header("Retry-After", "3")
		c.JSON(202, imageAsyncTaskResponse(task))
		return
	}
	if task.Status == "failed" {
		status := task.HTTPStatus
		if status < 400 || status > 599 {
			status = 502
		}
		imageAsyncError(c, status, task.ErrorCode, task.ErrorMessage)
		return
	}
	r := imageAsyncRuntime
	if r == nil || task.StorageNode != r.node {
		imageAsyncError(c, 503, "image_task_storage_unavailable", "Image task storage is unavailable on this node")
		return
	}
	path := filepath.Join(imageTaskDir(r.root, task.ID), "result.json")
	f, err := os.Open(path)
	if err != nil {
		imageAsyncError(c, 503, "image_task_result_unavailable", "Stored image result is temporarily unavailable")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		imageAsyncError(c, 503, "image_task_result_unavailable", "Unable to read stored result")
		return
	}
	c.Header("Content-Type", "application/json")
	c.Header(common.RequestIdKey, task.RequestID)
	http.ServeContent(c.Writer, c.Request, "result.json", info.ModTime(), f)
}
