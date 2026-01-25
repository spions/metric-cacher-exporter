package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	webflag "github.com/prometheus/exporter-toolkit/web/kingpinflag"
	"github.com/redis/go-redis/v9"
)

type redisClient interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Ping(ctx context.Context) *redis.StatusCmd
}

type HealthResponse struct {
	Status   string           `json:"status"`
	Services map[string]Stats `json:"services"`
}

type Stats struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

var (
	rdb    redisClient
	logger *slog.Logger

	// HTTP requests total hit/miss
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mce_http_requests_total",
		Help: "Total number of HTTP requests",
	}, []string{"upstream", "status", "cache_status"})

	// Total metrics hit/miss by upstream
	exportedMetricsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mce_cacher_exported_metrics_lines_total",
		Help: "Total number of individual metric lines processed by cacher",
	}, []string{"upstream", "cache_status"})

	cacheErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mce_cache_error_total",
		Help: "Total number of cache error",
	}, []string{"upstream"})

	toolkitFlags = webflag.AddFlags(kingpin.CommandLine, ":8080")

	redisAddr = kingpin.Flag(
		"redis.addr",
		"Hostname and port to use for connecting to Redis",
	).Default("localhost:6379").Envar("REDIS_ADDR").String()

	redisPassword = kingpin.Flag(
		"redis.password",
		"Password for Redis authentication",
	).Envar("REDIS_PASSWORD").String()

	redisDB = kingpin.Flag(
		"redis.db",
		"Redis database number (0-15)",
	).Default("0").Envar("REDIS_DB").Int()
)

func main() {
	// Parse flags.
	promslogConfig := &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.Version(version.Print("metric-cacher-exporter"))
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger = promslog.New(promslogConfig)

	logger.Info("Starting metric-cacher-exporter", "version", version.Info())
	logger.Info("Build context", "build_context", version.BuildContext())

	rdb = redis.NewClient(&redis.Options{
		Addr:     *redisAddr,
		Password: *redisPassword,
		DB:       *redisDB,
	})

	// Check connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Warn("Redis connection failed, but continuation is enabled. Working without cache.", "error", err)
	} else {
		logger.Info("Successfully connected to Redis", "redis", *redisAddr)
	}

	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) { readinessHandler(w, r, logger) }) // Ready to accept traffic
	http.HandleFunc("/healthy", livenessHandler)                                                               // Process health check
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", rootHandler)

	srv := &http.Server{}
	if err := web.ListenAndServe(srv, toolkitFlags, logger); err != nil {
		logger.Error("Error starting HTTP server", "err", err)
		os.Exit(1)
	}

}

func countMetricLines(data string) int {
	scanner := bufio.NewScanner(strings.NewReader(data))
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		// Count only lines that are not empty and not comments
		if line != "" && !strings.HasPrefix(line, "#") {
			count++
		}
	}
	return count
}

func fetchFromRedis(w http.ResponseWriter, r *http.Request, cacheTTL int, logger *slog.Logger) {

	//start := time.Now()
	path := r.URL.Path
	ctx := r.Context()

	ttlDuration := time.Duration(cacheTTL) * time.Second

	hash := sha256.Sum256([]byte(r.URL.String()))
	cacheKey := fmt.Sprintf("metrics:%s", hex.EncodeToString(hash[:]))
	lockKey := "lock:" + cacheKey

	// 1. Attempt to get data from cache
	val, err := rdb.Get(ctx, cacheKey).Result()

	if err == nil {
		// --- CASE 1: CACHE HIT (Data found) ---
		httpRequestsTotal.WithLabelValues(r.URL.Host, strconv.Itoa(http.StatusOK), "HIT").Inc()

		// Count number of metrics inside cached response
		mCount := countMetricLines(val)
		exportedMetricsTotal.WithLabelValues(r.URL.Host, "HIT").Add(float64(mCount))

		//cacheHits.WithLabelValues(r.URL.Host).Inc()
		//httpRequestDuration.WithLabelValues(r.URL.Host).Observe(time.Since(start).Seconds())

		writeResponse(w, []byte(val), "HIT")
		return
	}

	loopStart := time.Now()

	if errors.Is(err, redis.Nil) {
		// --- CASE 2: CACHE MISS (Key not found, this is normal) ---
		// Distributed coordination (one goes to API, others wait)
		for {
			// Attempt to acquire lock (SET NX)
			isLocked, _ := rdb.SetNX(ctx, lockKey, "processing", ttlDuration).Result()

			if isLocked {
				// Current worker became leader
				data, err := fetchFromUpstream(ctx, r, cacheKey, ttlDuration, logger)
				rdb.Del(ctx, lockKey) // Remove lock after completion

				if err != nil {
					logger.Warn("[Upstream] Error", "error", err)
					httpRequestsTotal.WithLabelValues(r.URL.Host, strconv.Itoa(http.StatusBadGateway), "MISS").Inc()
					http.Error(w, "[Upstream] Error\"", http.StatusBadGateway)
					return
				}
				// Count number of metrics inside cached response
				mCount := countMetricLines(string(data))
				exportedMetricsTotal.WithLabelValues(r.URL.Host, "MISS").Add(float64(mCount))

				httpRequestsTotal.WithLabelValues(r.URL.Host, strconv.Itoa(http.StatusOK), "MISS").Inc()
				//cacheMisses.WithLabelValues(r.URL.Host).Inc()

				//httpRequestDuration.WithLabelValues(r.URL.Host).Observe(time.Since(start).Seconds())
				writeResponse(w, data, "MISS")
				return
			}

			// If lock is occupied, wait and check cache
			select {
			case <-time.After(500 * time.Millisecond):
				val, err := rdb.Get(ctx, cacheKey).Result()
				if err == nil {
					// Count number of metrics inside cached response
					mCount := countMetricLines(val)
					exportedMetricsTotal.WithLabelValues(r.URL.Host, "HIT-DISTRIBUTED").Add(float64(mCount))

					httpRequestsTotal.WithLabelValues(r.URL.Host, strconv.Itoa(http.StatusOK), "HIT-DISTRIBUTED").Inc()
					//cacheHits.WithLabelValues(r.URL.Host).Inc()
					//httpRequestDuration.WithLabelValues(r.URL.Host).Observe(time.Since(start).Seconds())
					writeResponse(w, []byte(val), "HIT-DISTRIBUTED")
					return
				}
			case <-ctx.Done():
				http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
				return
			}

			waitDuration := time.Duration(float64(ttlDuration) * 0.9)

			// Prevent eternal waiting
			if time.Since(loopStart) > waitDuration {
				httpRequestsTotal.WithLabelValues(path, strconv.Itoa(http.StatusGatewayTimeout), "TIMEOUT").Inc()
				http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
				return
			}
		}
	} else {
		// --- CASE 3: REDIS ERROR (Redis is down or slow) ---
		logger.Error("Redis error during GET", "err", err, "key", cacheKey)
		cacheErrors.WithLabelValues(r.URL.Host).Inc()
		httpRequestsTotal.WithLabelValues(r.URL.Host, strconv.Itoa(http.StatusOK), "BYPASS").Inc()
		cacheErrors.WithLabelValues(r.URL.Host).Inc()

		data, _ := fetchFromUpstream(ctx, r, cacheKey, ttlDuration, logger)
		writeResponse(w, data, "MISS")
	}

}

func fetchFromUpstream(ctx context.Context, r *http.Request, cacheKey string, ttlDuration time.Duration, logger *slog.Logger) ([]byte, error) {

	clientTimeout := time.Duration(float64(ttlDuration) * 0.9)

	// Protection: timeout should not be zero, otherwise request will be "eternal"
	if clientTimeout <= 0 {
		clientTimeout = 2 * time.Second // or other default
	}

	client := &http.Client{Timeout: clientTimeout}

	// Clone original request with new context
	req := r.Clone(ctx)

	// IMPORTANT: When sending request by client, RequestURI field must be empty.
	// It is populated in server requests, but causes error in client ones.
	req.RequestURI = ""
	// Remove header so that http.Client manages compression itself.
	// It will add gzip to request and decompress response by itself.
	req.Header.Del("Accept-Encoding")

	//req, _ := http.NewRequestWithContext(ctx, "GET", r.URL.String(), nil)

	logger.Info("[Upstream] Fetching fresh data", "cache_key", cacheKey)
	resp, err := client.Do(req)
	if err != nil {
		//httpRequestsTotal.WithLabelValues(r.URL.Host, strconv.Itoa(http.StatusBadGateway), "MISS").Inc()
		//upstreamRequestsTotal.WithLabelValues("error").Inc()
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logger.Error("failed to close response body", "err", err)
		}
	}(resp.Body)

	//upstreamRequestsTotal.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Save result to Redis with TTL
	rdb.Set(ctx, cacheKey, data, ttlDuration)
	return data, nil
}

func writeResponse(w http.ResponseWriter, data []byte, cacheStatus string) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Header().Set("X-Cache", cacheStatus)
	_, _ = w.Write(data)
}

func livenessHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func readinessHandler(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	// Выполняем проверку
	err := rdb.Ping(ctx).Err()
	latency := time.Since(start).Milliseconds()

	// По умолчанию считаем, что всё хорошо
	status := "ok"
	redisStatus := "up"
	redisError := ""

	// Если Redis недоступен
	if err != nil {
		logger.Warn("[Readiness] Redis check failed (non-critical)", "error", err)

		// Меняем внутренние статусы, но НЕ меняем HTTP Code
		status = "degraded" // "деградирован" — сервис работает, но не в полную силу
		redisStatus = "down"
		redisError = err.Error()
	}

	response := HealthResponse{
		Status: status,
		Services: map[string]Stats{
			"redis": {
				Status:    redisStatus,
				LatencyMS: latency,
				Error:     redisError,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	// Всегда возвращаем 200 OK, чтобы балансировщик (k8s/nginx) не снимал трафик
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Error("Error encoding response", "error", err)
	}
}

func getQueryInt(params url.Values, key string, defaultValue int) int {
	v := params.Get(key)
	if v == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue
	}
	return value
}

func checkURL(input string) string {
	// 1. Check if protocol is specified (http:// or https://)
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return input
	}

	// 2. Check for port presence
	// Use net.SplitHostPort to correctly handle even IPv6
	_, _, err := net.SplitHostPort(input)
	hasPort := err == nil

	// If there is no protocol AND no port -> add http://
	if !hasPort {
		return "http://" + input
	}

	// If there is no protocol, but there IS a port (e.g., "example.com:8080")
	// Go http.Client will still throw "unsupported protocol scheme" error,
	// so it's worth adding default protocol here as well (usually http for ports)
	return "http://" + input
}

func rootHandler(w http.ResponseWriter, r *http.Request) {

	var indexTemplate = template.Must(template.New("index").Parse(`
	<html><body>
		<h1>Metric Cacher Service</h1>
		<p>Available Endpoints:</p>
		<ul>
			<li><a href="/ready">/ready</a> - Readiness probe (checks Redis connection)</li>
			<li><a href="/healthy">/healthy</a> - Liveness probe (checks process health)</li>
			<li><a href="/metrics">/metrics</a> - Internal service metrics in Prometheus format</li>
		</ul>
	</body></html>
	`))

	path := r.URL.Path
	params := r.URL.Query()
	target := params.Get("target")
	// For cache TTL, during which data is stored.
	cacheTTL := getQueryInt(params, "cacheTTL", 60)

	if path == "/" && target == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := indexTemplate.Execute(w, nil)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	} else if target != "" {

		parsedTarget, err := url.Parse(checkURL(target))
		if err != nil {
			http.Error(w, "Error: Invalid URL", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		// Merge parameters: take current parameters of target site
		// and add those that came from request in params
		// get existing parameters from target (e.g., data=1)
		targetQuery := parsedTarget.Query()

		// Add parameters from incoming request (params)
		for key, values := range params {
			// Important: exclude 'target' parameter itself so it's not duplicated in final string
			switch key {
			case "target", "cacheTTL":
				continue
			}
			for _, v := range values {
				targetQuery.Add(key, v)
			}
		}

		// Encode parameters and write them back to RawQuery
		parsedTarget.RawQuery = targetQuery.Encode()
		parsedTarget.Path = path

		// Clone request
		proxyReq := r.Clone(r.Context())

		// Replace URL in clone
		proxyReq.URL = parsedTarget
		proxyReq.Host = parsedTarget.Host
		proxyReq.RequestURI = ""
		fetchFromRedis(w, proxyReq, cacheTTL, logger)

	} else {

		logger.Error("Error: Target is required", "target", target)
		http.Error(w, "Error: Target %s is required", http.StatusBadRequest)
		return
	}

}
