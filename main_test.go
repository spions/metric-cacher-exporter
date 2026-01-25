package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func init() {
	logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
}

// MockRedisClient simulates Redis client behavior
type MockRedisClient struct {
	mock.Mock
}

func (m *MockRedisClient) Get(ctx context.Context, key string) *redis.StringCmd {
	args := m.Called(ctx, key)
	cmd := redis.NewStringResult(args.String(0), args.Error(1))
	return cmd
}

func (m *MockRedisClient) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd {
	args := m.Called(ctx, key, value, expiration)
	cmd := redis.NewStatusResult("", args.Error(0))
	return cmd
}

func (m *MockRedisClient) SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd {
	args := m.Called(ctx, key, value, expiration)
	cmd := redis.NewBoolResult(args.Bool(0), args.Error(1))
	return cmd
}

func (m *MockRedisClient) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	args := m.Called(ctx, keys)
	cmd := redis.NewIntResult(int64(args.Int(0)), args.Error(1))
	return cmd
}

func (m *MockRedisClient) Ping(ctx context.Context) *redis.StatusCmd {
	args := m.Called(ctx)
	cmd := redis.NewStatusResult("", args.Error(0))
	return cmd
}

func getCacheKey(u string) string {
	hash := sha256.Sum256([]byte(u))
	return fmt.Sprintf("metrics:%s", hex.EncodeToString(hash[:]))
}

func TestMetricsHandler_InvalidParameters(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	// Configure mock to not fail when Get is called
	mockRedis.On("Get", mock.Anything, mock.Anything).Return("", redis.Nil).Maybe()
	mockRedis.On("SetNX", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()
	mockRedis.On("Set", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockRedis.On("Del", mock.Anything, mock.Anything).Return(1, nil).Maybe()

	// Create request with missing parameters (only target)
	req := httptest.NewRequest("GET", "/monitoring/v2/prometheusMetrics?target=http://example.com", nil)
	w := httptest.NewRecorder()

	// Call handler
	rootHandler(w, req)
}

func TestMetricsHandler_ValidRequest_CacheHit(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	// URL is reconstructed in rootHandler.
	// Original URL: /monitoring/v2/prometheusMetrics?target=http://example.com&folderId=test-folder&service=test-service
	// Target URL will be: http://example.com/monitoring/v2/prometheusMetrics?folderId=test-folder&service=test-service
	targetURL := "http://example.com/monitoring/v2/prometheusMetrics?folderId=test-folder&service=test-service"
	cacheKey := getCacheKey(targetURL)

	// Set expectations for mock
	mockRedis.On("Get", mock.Anything, cacheKey).Return("mocked metrics data", nil)

	// Create request with valid parameters
	req := httptest.NewRequest("GET", "/monitoring/v2/prometheusMetrics?target=http://example.com&folderId=test-folder&service=test-service", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	// Call handler
	rootHandler(w, req)

	// Check result
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "mocked metrics data", w.Body.String())
	assert.Equal(t, "HIT", w.Header().Get("X-Cache"))
	assert.Equal(t, "text/plain; version=0.0.4", w.Header().Get("Content-Type"))

	// Check that expected methods were called
	mockRedis.AssertExpectations(t)
}

func TestMetricsHandler_CacheMiss_UpstreamSuccess(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("test_metric 42\n"))
	}))
	defer ts.Close()

	// Parameters in request will be sorted when forming target URL
	targetURL := ts.URL + "/monitoring/v2/prometheusMetrics?folderId=test-folder&service=test-service"
	cacheKey := getCacheKey(targetURL)
	lockKey := "lock:" + cacheKey

	// Configure mock to return error on Get (cache miss)
	mockRedis.On("Get", mock.Anything, cacheKey).Return("", redis.Nil)

	// Configure mock for successful SetNX (acquiring lock)
	mockRedis.On("SetNX", mock.Anything, lockKey, "processing", 60*time.Second).Return(true, nil)

	// Configure mock for successful Set (saving to cache)
	mockRedis.On("Set", mock.Anything, cacheKey, mock.Anything, 60*time.Second).Return(nil)

	// Configure mock for lock removal
	mockRedis.On("Del", mock.Anything, []string{lockKey}).Return(1, nil)

	// Create request
	req := httptest.NewRequest("GET", "/monitoring/v2/prometheusMetrics?target="+ts.URL+"&folderId=test-folder&service=test-service", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	// Call handler
	rootHandler(w, req)

	// Check result
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "test_metric 42")
	assert.Equal(t, "MISS", w.Header().Get("X-Cache"))

	// Check that expected methods were called
	mockRedis.AssertExpectations(t)
}

func TestMetricsHandler_UpstreamError(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("Upstream error"))
	}))
	defer ts.Close()

	targetURL := ts.URL + "/monitoring/v2/prometheusMetrics?folderId=test-folder&service=test-service"
	cacheKey := getCacheKey(targetURL)
	lockKey := "lock:" + cacheKey

	// Configure mock to return error on Get (cache miss)
	mockRedis.On("Get", mock.Anything, cacheKey).Return("", redis.Nil)

	// Configure mock for successful SetNX (acquiring lock)
	mockRedis.On("SetNX", mock.Anything, lockKey, "processing", 60*time.Second).Return(true, nil)

	// Configure mock for lock removal
	mockRedis.On("Del", mock.Anything, []string{lockKey}).Return(1, nil)

	// Create request
	req := httptest.NewRequest("GET", "/monitoring/v2/prometheusMetrics?target="+ts.URL+"&folderId=test-folder&service=test-service", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	// Call handler
	rootHandler(w, req)

	// Check result
	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Contains(t, w.Body.String(), "[Upstream] Error")

	// Check that expected methods were called
	mockRedis.AssertExpectations(t)
}

func TestMetricsHandler_ConcurrentRequests(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# HELP test_metric Test metric\n# TYPE test_metric gauge\ntest_metric 42\n"))
	}))
	defer ts.Close()

	targetURL := ts.URL + "/monitoring/v2/prometheusMetrics?folderId=test-folder&service=test-service"
	cacheKey := getCacheKey(targetURL)
	lockKey := "lock:" + cacheKey

	// Configure mock to return error on Get (first 3 calls - miss, then - hit)
	mockRedis.On("Get", mock.Anything, cacheKey).Return("", redis.Nil).Times(3)
	mockRedis.On("Get", mock.Anything, cacheKey).Return("test_metric 42\n", nil)

	// Configure mock for first request - successful lock acquisition
	mockRedis.On("SetNX", mock.Anything, lockKey, "processing", 60*time.Second).Return(true, nil).Once()

	// Configure mock for subsequent requests - failed lock acquisition (repeatable)
	mockRedis.On("SetNX", mock.Anything, lockKey, "processing", 60*time.Second).Return(false, nil)

	// Configure mock for successful Set (saving to cache)
	mockRedis.On("Set", mock.Anything, cacheKey, mock.Anything, 60*time.Second).Return(nil)

	// Configure mock for lock removal
	mockRedis.On("Del", mock.Anything, []string{lockKey}).Return(1, nil)

	// Create multiple requests to simulate concurrent access
	requests := make([]*http.Request, 3)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/monitoring/v2/prometheusMetrics?target="+ts.URL+"&folderId=test-folder&service=test-service", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		requests[i] = req
	}

	// Execute requests concurrently
	responses := make([]*httptest.ResponseRecorder, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		responses[i] = httptest.NewRecorder()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rootHandler(responses[i], requests[i])
		}(i)
	}

	// Wait for all requests to complete
	wg.Wait()

	// Check results
	missCount := 0
	hitDistributedCount := 0
	for i, resp := range responses {
		assert.Equal(t, http.StatusOK, resp.Code, "Request %d should succeed", i)
		cacheStatus := resp.Header().Get("X-Cache")
		switch cacheStatus {
		case "MISS":
			missCount++
		case "HIT-DISTRIBUTED":
			hitDistributedCount++
		}
	}

	assert.Equal(t, 1, missCount, "Should have exactly 1 MISS")
	assert.Equal(t, 2, hitDistributedCount, "Should have exactly 2 HIT-DISTRIBUTED")

	// Check that expected methods were called
	mockRedis.AssertExpectations(t)
}

func TestMetricsHandler_Timeout(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	targetURL := "http://example.com/monitoring/v2/prometheusMetrics?folderId=test-folder&service=test-service"
	cacheKey := getCacheKey(targetURL)
	lockKey := "lock:" + cacheKey

	// Configure mock to return error on Get (cache miss)
	mockRedis.On("Get", mock.Anything, cacheKey).Return("", redis.Nil)

	// Configure mock for failed lock acquisition
	mockRedis.On("SetNX", mock.Anything, lockKey, "processing", 60*time.Second).Return(false, nil)

	// Configure mock for cache check - always return miss to simulate waiting
	mockRedis.On("Get", mock.Anything, cacheKey).Return("", redis.Nil)

	// Create request
	req := httptest.NewRequest("GET", "/monitoring/v2/prometheusMetrics?target=http://example.com&folderId=test-folder&service=test-service", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	// Create context with short timeout to speed up test
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	// Call handler
	rootHandler(w, req)

	// Check result
	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
	assert.Contains(t, w.Body.String(), "Gateway Timeout")
}

func TestFetchFromUpstream_Success(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# HELP test_metric Test metric\n# TYPE test_metric gauge\ntest_metric 42\n"))
	}))
	defer ts.Close()

	// In fetchFromUpstream URL is taken from req.URL.String()
	// Parameters will be sorted if they are reconstructed in rootHandler,
	// but here we call fetchFromUpstream directly.
	query := "folderId=test-folder&service=test-service"
	targetURL := ts.URL + "/metrics?" + query
	cacheKey := getCacheKey(targetURL)

	// Configure mock for successful save to cache
	mockRedis.On("Set", mock.Anything, cacheKey, mock.Anything, 1*time.Minute).Return(nil)

	// Call function
	req := httptest.NewRequest("GET", targetURL, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	data, err := fetchFromUpstream(context.Background(), req, cacheKey, 1*time.Minute, logger)

	// Check result
	assert.NoError(t, err)
	assert.NotNil(t, data)
	assert.Contains(t, string(data), "test_metric 42")
}

func TestFetchFromUpstream_UpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}))
	defer ts.Close()

	query := "folderId=test-folder&service=test-service"
	targetURL := ts.URL + "/metrics?" + query
	cacheKey := getCacheKey(targetURL)

	// Call function
	req := httptest.NewRequest("GET", targetURL, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	data, err := fetchFromUpstream(context.Background(), req, cacheKey, 1*time.Minute, logger)

	// Check result
	assert.Error(t, err)
	assert.Nil(t, data)
	assert.Contains(t, err.Error(), "upstream status 404")
}

func TestFetchFromUpstream_NetworkError(t *testing.T) {
	// To simulate network error (e.g., unable to connect),
	// we can use a deliberately invalid URL.
	targetURL := "http://invalid-domain-that-should-not-exist.local/metrics"
	cacheKey := getCacheKey(targetURL)

	// Call function
	req := httptest.NewRequest("GET", targetURL, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	data, err := fetchFromUpstream(context.Background(), req, cacheKey, 1*time.Minute, logger)

	// Check result
	assert.Error(t, err)
	assert.Nil(t, data)
}

func TestGetEnv(t *testing.T) {
	// В данном проекте функция GetEnv отсутствует в основном коде,
	// и тесты были закомментированы. Оставляем тест пустым или удаляем,
	// так как main.go трогать нельзя для добавления функции.
}

func TestReadinessHandler_Success(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	// Set expectations for mock
	mockRedis.On("Ping", mock.Anything).Return(nil)

	// Create request
	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	// Call handler
	readinessHandler(w, req, logger)

	// Check result
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"status\":\"ok\"")

	// Check that expected methods were called
	mockRedis.AssertExpectations(t)
}

func TestReadinessHandler_Failure(t *testing.T) {
	// Create mock Redis client
	mockRedis := new(MockRedisClient)
	rdb = mockRedis

	// Set expectations for mock - Ping error
	mockRedis.On("Ping", mock.Anything).Return(fmt.Errorf("redis connection lost"))

	// Create request
	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	// Call handler
	readinessHandler(w, req, logger)

	// Check result
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"status\":\"degraded\"")
	assert.Contains(t, w.Body.String(), "redis connection lost")

	// Check that expected methods were called
	mockRedis.AssertExpectations(t)
}

func TestRootHandler(t *testing.T) {
	t.Run("Root path success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()

		rootHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	})

	t.Run("Bad request for other paths without target", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/non-existent", nil)
		w := httptest.NewRecorder()

		rootHandler(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
