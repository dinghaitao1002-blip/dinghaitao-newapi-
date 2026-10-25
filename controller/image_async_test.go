package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestImageAsyncResponseIsDurableBeforePublish(t *testing.T) {
	dir := t.TempDir()
	const body = `{"created":123,"data":[{"b64_json":"aGVsbG8="}]}`
	w, err := newImageTaskResponseWriter(dir, int64(len(body)))
	require.NoError(t, err)
	t.Cleanup(w.Close)
	w.Header().Set("Content-Type", "application/json")
	_, err = io.WriteString(w, body)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "result.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, w.Commit())
	require.NoError(t, w.Commit())
	stored, err := os.ReadFile(filepath.Join(dir, "result.json"))
	require.NoError(t, err)
	assert.Equal(t, body, string(stored))
	info, err := os.Stat(filepath.Join(dir, "result.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	_, err = w.Write([]byte("extra"))
	assert.Error(t, err)
}

func TestImageAsyncResponseRejectsOversizeAndStreaming(t *testing.T) {
	for _, test := range []struct {
		name, contentType, body string
		limit                   int64
		writeError              bool
	}{
		{name: "storage overflow", contentType: "application/json", body: "too large", limit: 3, writeError: true},
		{name: "SSE cannot be a finished image", contentType: "text/event-stream", body: "data: {}\n\n", limit: 100},
		{name: "empty upstream body", contentType: "application/json", body: "", limit: 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			w, err := newImageTaskResponseWriter(dir, test.limit)
			require.NoError(t, err)
			t.Cleanup(w.Close)
			w.Header().Set("Content-Type", test.contentType)
			_, err = io.WriteString(w, test.body)
			if test.writeError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Error(t, w.Commit())
			_, err = os.Stat(filepath.Join(dir, "result.json"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func imageAsyncMultipartFixture(t *testing.T, boundary, image string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.SetBoundary(boundary))
	require.NoError(t, w.WriteField("model", "async-fixture"))
	require.NoError(t, w.WriteField("prompt", "a blue circle"))
	part, err := w.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = io.WriteString(part, image)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return body.Bytes(), w.FormDataContentType()
}

func TestImageAsyncFingerprintIgnoresBoundaryButBindsPayloadAndHeaders(t *testing.T) {
	bodyA, typeA := imageAsyncMultipartFixture(t, "boundary-a", "same image")
	bodyB, typeB := imageAsyncMultipartFixture(t, "boundary-b", "same image")
	bodyC, typeC := imageAsyncMultipartFixture(t, "boundary-c", "changed image")
	hashA, err := imageTaskFingerprint(bytes.NewReader(bodyA), typeA, "/v1/images/edits", `{"X-Size-Fit":"original"}`)
	require.NoError(t, err)
	hashB, err := imageTaskFingerprint(bytes.NewReader(bodyB), typeB, "/v1/images/edits", `{"X-Size-Fit":"original"}`)
	require.NoError(t, err)
	assert.Equal(t, hashA, hashB)
	hashC, err := imageTaskFingerprint(bytes.NewReader(bodyC), typeC, "/v1/images/edits", `{"X-Size-Fit":"original"}`)
	require.NoError(t, err)
	assert.NotEqual(t, hashA, hashC)
	hashHeader, err := imageTaskFingerprint(bytes.NewReader(bodyA), typeA, "/v1/images/edits", `{"X-Size-Fit":"resize"}`)
	require.NoError(t, err)
	assert.NotEqual(t, hashA, hashHeader)
	hashPath, err := imageTaskFingerprint(bytes.NewReader(bodyA), typeA, "/v1/images/generations", `{"X-Size-Fit":"original"}`)
	require.NoError(t, err)
	assert.NotEqual(t, hashA, hashPath)
}

func setupImageAsyncControllerFixture(t *testing.T) (*gorm.DB, *imageAsyncRunner) {
	t.Helper()
	previousDB, previousLogDB, previousRuntime := model.DB, model.LOG_DB, imageAsyncRuntime
	previousRedis, previousBatch, previousMemory := common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled
	previousLog, previousExport, previousRetry := common.LogConsumeEnabled, common.DataExportEnabled, common.RetryTimes
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	initModelListColumnNames(t)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled = false, false, false
	common.LogConsumeEnabled, common.DataExportEnabled, common.RetryTimes = true, false, 0
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.ImageAsyncTask{}, &model.ImageAsyncQueueLock{}, &model.User{}, &model.Token{},
		&model.Channel{}, &model.Ability{}, &model.Log{}, &model.UserModelPrice{}, &model.UserModelImageQualityPrice{}, &model.ModelImageQualityPrice{}))
	model.DB, model.LOG_DB = db, db
	ctx, cancel := context.WithCancel(context.Background())
	r := &imageAsyncRunner{root: t.TempDir(), node: "image-async-test-node", workers: 1, queueLimit: 10, userQueueLimit: 5,
		requestLimit: 1 << 20, resultLimit: 1 << 20, storageLimit: 10 << 20, timeout: 5 * time.Second, retention: 24 * time.Hour,
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	imageAsyncRuntime = r
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() {
		cancel()
		r.wg.Wait()
		model.DB, model.LOG_DB, imageAsyncRuntime = previousDB, previousLogDB, previousRuntime
		common.RedisEnabled, common.BatchUpdateEnabled, common.MemoryCacheEnabled = previousRedis, previousBatch, previousMemory
		common.LogConsumeEnabled, common.DataExportEnabled, common.RetryTimes = previousLog, previousExport, previousRetry
		common.SetDatabaseTypes(previousMainType, previousLogType)
		require.NoError(t, sqlDB.Close())
	})
	return db, r
}

func TestImageAsyncOptInKeepsSynchronousValidation(t *testing.T) {
	_, _ = setupImageAsyncControllerFixture(t)
	for _, test := range []struct {
		name, path, prefer, body string
		status                   int
		code                     string
	}{
		{"async stream rejected", "/v1/images/generations?async=true", "", `{"model":"async-fixture","prompt":"circle","stream":true}`, 400, "async_stream_not_supported"},
		{"prefer stream rejected", "/v1/images/generations", "respond-async", `{"model":"async-fixture","prompt":"circle","stream":true}`, 400, "async_stream_not_supported"},
		{"ordinary request remains synchronous", "/v1/images/generations", "", `{}`, 0, "model is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Prefer", test.prefer)
			defer common.CleanupBodyStorage(c)
			RelayImage(c)
			if test.status == 0 {
				baseline := httptest.NewRecorder()
				baselineContext, _ := gin.CreateTestContext(baseline)
				baselineContext.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
				baselineContext.Request.Header.Set("Content-Type", "application/json")
				defer common.CleanupBodyStorage(baselineContext)
				Relay(baselineContext, types.RelayFormatOpenAIImage)
				assert.Equal(t, baseline.Code, w.Code)
				assert.JSONEq(t, baseline.Body.String(), w.Body.String())
			} else {
				assert.Equal(t, test.status, w.Code)
			}
			assert.Contains(t, w.Body.String(), test.code)
		})
	}
}

func TestImageAsyncRetryRequiresAnUnambiguousUnwrittenFailure(t *testing.T) {
	for _, test := range []struct {
		name                           string
		status                         int
		code                           types.ErrorCode
		cancel, written, skip, allowed bool
	}{
		{name: "transport outcome is ambiguous", status: 502, code: types.ErrorCodeDoRequestFailed},
		{name: "billing or storage failed after generation", status: 500, code: types.ErrorCodeUpdateDataError},
		{name: "response already started", status: 502, code: types.ErrorCodeBadResponseBody, written: true},
		{name: "deadline expired", status: 504, code: types.ErrorCodeBadResponseBody, cancel: true},
		{name: "provider explicitly forbids retry", status: 503, code: types.ErrorCodeBadResponseBody, skip: true},
		{name: "upstream rate limit allows existing failover", status: 429, code: types.ErrorCodeBadResponseBody, allowed: true},
		{name: "upstream exhausted quota allows existing failover", status: 402, code: types.ErrorCodeBadResponseBody, allowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
			if test.cancel {
				cancel()
			}
			if test.written {
				_, err := c.Writer.Write([]byte(`{"data":[`))
				require.NoError(t, err)
			}
			var options []types.NewAPIErrorOptions
			if test.skip {
				options = append(options, types.ErrOptionWithSkipRetry())
			}
			apiErr := types.NewErrorWithStatusCode(errors.New("fixture failure"), test.code, test.status, options...)
			assert.Equal(t, test.allowed, canRetryAsyncImage(c, apiErr))
		})
	}
}

func TestImageAsyncWorkerAndResultDoNotRegenerateOrRebill(t *testing.T) {
	db, r := setupImageAsyncControllerFixture(t)
	previousPrices := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"async-fixture":0.01}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices)) })
	service.InitHttpClient()
	const initialQuota = 1000000
	user := model.User{Username: "async-fixture-user", Status: common.UserStatusEnabled, Role: common.RoleAdminUser, Group: "default", Quota: initialQuota, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "asyncfixturetoken", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: initialQuota}
	require.NoError(t, db.Create(&token).Error)
	secondToken := model.Token{UserId: user.Id, Key: "asyncfixturetokenother", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: initialQuota}
	require.NoError(t, db.Create(&secondToken).Error)
	const result = `{"created":123,"data":[{"b64_json":"aGVsbG8="}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}`
	var calls atomic.Int32
	var receivedEditImage atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.URL.Path == "/v1/images/edits" {
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, "invalid multipart", 400)
				return
			}
			defer req.MultipartForm.RemoveAll()
			file, _, err := req.FormFile("image")
			if err != nil {
				http.Error(w, "missing reference image", 400)
				return
			}
			defer file.Close()
			image, err := io.ReadAll(file)
			if err != nil {
				http.Error(w, "unreadable reference image", 400)
				return
			}
			receivedEditImage.Store(string(image))
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, result)
			return
		}
		if req.URL.Path != "/v1/images/generations" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(req.Body)
		if strings.Contains(string(body), `"prompt":"reject"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"bad_request","message":"fixture rejection"}}`)
			return
		}
		_, _ = io.WriteString(w, result)
	}))
	t.Cleanup(upstream.Close)
	channel := model.Channel{Type: constant.ChannelTypeOpenAI, Name: "async-fixture-channel", Key: "upstream-secret", Status: common.ChannelStatusEnabled, Models: "async-fixture", Group: "default", BaseURL: &upstream.URL}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, channel.UpdateAbilities(nil))
	engine := gin.New()
	require.NoError(t, engine.SetTrustedProxies(nil))
	engine.Use(middleware.BodyStorageCleanup())
	engine.POST("/v1/images/generations", middleware.TokenAuth(), middleware.Distribute(), RelayImage)
	engine.POST("/v1/images/edits", middleware.TokenAuth(), middleware.Distribute(), RelayImage)
	engine.GET("/v1/images/tasks/:task_id", middleware.TokenAuthReadOnly(), middleware.ImageTaskReadAuth(), GetImageAsyncTask)
	engine.GET("/v1/images/tasks/:task_id/result", middleware.TokenAuthReadOnly(), middleware.ImageTaskReadAuth(), GetImageAsyncTaskResult)
	authorization := "Bearer sk-" + token.Key + fmt.Sprintf("-%d", channel.Id)
	ctx, cancel := context.WithCancel(context.Background())
	submit := httptest.NewRequest(http.MethodPost, "/v1/images/generations?async=true", strings.NewReader(`{"model":"async-fixture","prompt":"a blue circle","n":1,"response_format":"b64_json"}`)).WithContext(ctx)
	submit.Header.Set("Authorization", authorization)
	submit.Header.Set("Content-Type", "application/json")
	submit.Header.Set("Idempotency-Key", "one-image")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, submit)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var accepted struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &accepted))
	require.Equal(t, "queued", accepted.Status)
	cancel() // Cancelling the submitting client must not cancel a persisted job.
	replay := httptest.NewRequest(http.MethodPost, "/v1/images/generations?async=true", strings.NewReader(`{"model":"async-fixture","prompt":"a blue circle","n":1,"response_format":"b64_json"}`))
	replay.Header.Set("Authorization", authorization)
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Idempotency-Key", "one-image")
	replayResponse := httptest.NewRecorder()
	engine.ServeHTTP(replayResponse, replay)
	require.Equal(t, http.StatusAccepted, replayResponse.Code, replayResponse.Body.String())
	var duplicate struct {
		ID string `json:"id"`
	}
	require.NoError(t, common.Unmarshal(replayResponse.Body.Bytes(), &duplicate))
	assert.Equal(t, accepted.ID, duplicate.ID)
	get := func(key, suffix string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/images/tasks/"+accepted.ID+suffix, nil)
		req.Header.Set("Authorization", "Bearer sk-"+key)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, req)
		return response
	}
	require.Equal(t, http.StatusAccepted, get(token.Key, "/result").Code)
	require.Equal(t, http.StatusNotFound, get(secondToken.Key, "").Code)
	require.Equal(t, http.StatusNotFound, get(secondToken.Key, "/result").Code)
	claimed, err := model.ClaimImageAsyncTask(r.node, "fixture-worker", 90)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	r.execute(claimed, "fixture-worker")
	stored, err := model.GetImageAsyncTask(accepted.ID, user.Id, token.Id)
	require.NoError(t, err)
	require.Equal(t, model.ImageAsyncStatusSucceeded, stored.Status, "%+v", stored)
	require.Equal(t, model.ImageAsyncBillingSettled, stored.BillingState)
	require.Greater(t, stored.ActualQuota, 0)
	for i := 0; i < 2; i++ {
		resultResponse := get(token.Key, "/result")
		require.Equal(t, http.StatusOK, resultResponse.Code, resultResponse.Body.String())
		assert.Equal(t, result, resultResponse.Body.String())
		assert.Equal(t, "no-store", resultResponse.Header().Get("Cache-Control"))
	}
	assert.Equal(t, int32(1), calls.Load())
	var currentUser model.User
	var currentToken model.Token
	require.NoError(t, db.First(&currentUser, user.Id).Error)
	require.NoError(t, db.First(&currentToken, token.Id).Error)
	assert.Equal(t, initialQuota-stored.ActualQuota, currentUser.Quota)
	assert.Equal(t, initialQuota-stored.ActualQuota, currentToken.RemainQuota)
	assert.Equal(t, stored.ActualQuota, currentToken.UsedQuota)
	var logCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)

	// A real upstream failure must refund the reservation and must not affect
	// the already successful job or add a second consume log.
	rejectedRequest := httptest.NewRequest(http.MethodPost, "/v1/images/generations?async=true", strings.NewReader(`{"model":"async-fixture","prompt":"reject","n":1}`))
	rejectedRequest.Header.Set("Authorization", authorization)
	rejectedRequest.Header.Set("Content-Type", "application/json")
	rejectedResponse := httptest.NewRecorder()
	engine.ServeHTTP(rejectedResponse, rejectedRequest)
	require.Equal(t, http.StatusAccepted, rejectedResponse.Code, rejectedResponse.Body.String())
	failedTask, err := model.ClaimImageAsyncTask(r.node, "failed-worker", 90)
	require.NoError(t, err)
	require.NotNil(t, failedTask)
	r.execute(failedTask, "failed-worker")
	failedTask, err = model.GetImageAsyncTask(failedTask.ID, user.Id, token.Id)
	require.NoError(t, err)
	require.Equal(t, model.ImageAsyncStatusFailed, failedTask.Status, "%+v", failedTask)
	assert.Equal(t, model.ImageAsyncBillingRefunded, failedTask.BillingState)
	assert.Equal(t, http.StatusBadRequest, failedTask.HTTPStatus)
	require.Greater(t, failedTask.ReservedQuota, 0)
	require.NoError(t, db.First(&currentUser, user.Id).Error)
	require.NoError(t, db.First(&currentToken, token.Id).Error)
	assert.Equal(t, initialQuota-stored.ActualQuota, currentUser.Quota)
	assert.Equal(t, initialQuota-stored.ActualQuota, currentToken.RemainQuota)
	assert.Equal(t, int32(2), calls.Load())
	require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)

	// Even with retries enabled, failing to persist a produced response must
	// not generate again on another channel or settle the customer charge.
	common.RetryTimes = 3
	r.resultLimit = 10
	overflowRequest := httptest.NewRequest(http.MethodPost, "/v1/images/generations?async=true", strings.NewReader(`{"model":"async-fixture","prompt":"storage failure","n":1}`))
	overflowRequest.Header.Set("Authorization", authorization)
	overflowRequest.Header.Set("Content-Type", "application/json")
	overflowResponse := httptest.NewRecorder()
	engine.ServeHTTP(overflowResponse, overflowRequest)
	require.Equal(t, http.StatusAccepted, overflowResponse.Code, overflowResponse.Body.String())
	overflowTask, err := model.ClaimImageAsyncTask(r.node, "overflow-worker", 90)
	require.NoError(t, err)
	require.NotNil(t, overflowTask)
	r.execute(overflowTask, "overflow-worker")
	overflowTask, err = model.GetImageAsyncTask(overflowTask.ID, user.Id, token.Id)
	require.NoError(t, err)
	assert.Equal(t, model.ImageAsyncStatusFailed, overflowTask.Status)
	assert.Equal(t, model.ImageAsyncBillingRefunded, overflowTask.BillingState)
	assert.Equal(t, int32(3), calls.Load())
	require.NoError(t, db.First(&currentUser, user.Id).Error)
	require.NoError(t, db.First(&currentToken, token.Id).Error)
	assert.Equal(t, initialQuota-stored.ActualQuota, currentUser.Quota)
	assert.Equal(t, initialQuota-stored.ActualQuota, currentToken.RemainQuota)

	// Multipart references must survive the submitting request's form cleanup
	// and be replayed intact, just like the JSON generation request above.
	r.resultLimit = 1 << 20
	editBody, editContentType := imageAsyncMultipartFixture(t, "edit-after-cleanup", "original-reference-image")
	editRequest := httptest.NewRequest(http.MethodPost, "/v1/images/edits?async=true", bytes.NewReader(editBody))
	editRequest.Header.Set("Authorization", authorization)
	editRequest.Header.Set("Content-Type", editContentType)
	editResponse := httptest.NewRecorder()
	engine.ServeHTTP(editResponse, editRequest)
	require.Equal(t, http.StatusAccepted, editResponse.Code, editResponse.Body.String())
	editTask, err := model.ClaimImageAsyncTask(r.node, "edit-worker", 90)
	require.NoError(t, err)
	require.NotNil(t, editTask)
	r.execute(editTask, "edit-worker")
	editTask, err = model.GetImageAsyncTask(editTask.ID, user.Id, token.Id)
	require.NoError(t, err)
	require.Equal(t, model.ImageAsyncStatusSucceeded, editTask.Status, "%+v", editTask)
	assert.Equal(t, "original-reference-image", receivedEditImage.Load())
	assert.Equal(t, int32(4), calls.Load())
	require.NoError(t, db.First(&currentUser, user.Id).Error)
	assert.Equal(t, initialQuota-stored.ActualQuota-editTask.ActualQuota, currentUser.Quota)

	// Capacity can disappear after admission while a job waits in the queue.
	// Rechecking before execution must avoid generating or charging an image
	// whose result cannot fit in storage.
	storageRequest := httptest.NewRequest(http.MethodPost, "/v1/images/generations?async=true", strings.NewReader(`{"model":"async-fixture","prompt":"storage filled while queued","n":1}`))
	storageRequest.Header.Set("Authorization", authorization)
	storageRequest.Header.Set("Content-Type", "application/json")
	storageResponse := httptest.NewRecorder()
	engine.ServeHTTP(storageResponse, storageRequest)
	require.Equal(t, http.StatusAccepted, storageResponse.Code, storageResponse.Body.String())
	r.storageLimit = 1
	storageTask, err := model.ClaimImageAsyncTask(r.node, "storage-full-worker", 90)
	require.NoError(t, err)
	require.NotNil(t, storageTask)
	r.execute(storageTask, "storage-full-worker")
	storageTask, err = model.GetImageAsyncTask(storageTask.ID, user.Id, token.Id)
	require.NoError(t, err)
	assert.Equal(t, model.ImageAsyncStatusFailed, storageTask.Status)
	assert.Equal(t, http.StatusServiceUnavailable, storageTask.HTTPStatus)
	assert.Equal(t, "image_task_storage_full", storageTask.ErrorCode)
	assert.Empty(t, storageTask.BillingState)
	assert.Zero(t, storageTask.ReservedQuota)
	assert.Zero(t, storageTask.ActualQuota)
	assert.Equal(t, int32(4), calls.Load())
	require.NoError(t, db.First(&currentUser, user.Id).Error)
	require.NoError(t, db.First(&currentToken, token.Id).Error)
	assert.Equal(t, initialQuota-stored.ActualQuota-editTask.ActualQuota, currentUser.Quota)
	assert.Equal(t, initialQuota-stored.ActualQuota-editTask.ActualQuota, currentToken.RemainQuota)

	// Consuming the last token quota must not prevent retrieving an already
	// purchased image; explicit token revocation must still prevent access.
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{"remain_quota": 0, "status": common.TokenStatusExhausted}).Error)
	assert.Equal(t, http.StatusOK, get(token.Key, "/result").Code)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("expired_time", time.Now().Unix()-1).Error)
	assert.Equal(t, http.StatusUnauthorized, get(token.Key, "/result").Code)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(map[string]any{"expired_time": -1, "allow_ips": "127.0.0.1"}).Error)
	assert.Equal(t, http.StatusForbidden, get(token.Key, "/result").Code)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("allow_ips", "").Error)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("status", common.TokenStatusDisabled).Error)
	assert.Equal(t, http.StatusUnauthorized, get(token.Key, "/result").Code)
}
