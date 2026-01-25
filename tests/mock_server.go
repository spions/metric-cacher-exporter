//go:build ignore

package main

import (
	"fmt"
	"log"
	"math/rand"
	"metric-cacher/config"
	"net/http"
	"time"
)

func main() {
	port := config.GetEnv("MOCK_PORT", "8081")
	host := config.GetEnv("MOCK_HOST", "localhost")

	http.HandleFunc("/", mockMetricsHandler)

	log.Printf("Mock API server started on http://%s:%s", host, port)

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func mockMetricsHandler(w http.ResponseWriter, r *http.Request) {
	// Имитируем задержку сети (например, от 5 до 10 секунд)
	delay := time.Duration(200+rand.Intn(1000)) * time.Millisecond
	time.Sleep(delay)

	log.Printf("[%s] Received request from %s: %s %s?%s",
		time.Now().Format("2006-01-02 15:04:05"),
		r.RemoteAddr,
		r.Method,
		r.URL.Path,
		r.URL.RawQuery)

	log.Printf("Headers: %v", r.Header)

	// Формируем "рандомные" метрики в формате Prometheus
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	_, err := fmt.Fprintf(w,
		"# TYPE mock_cpu_usage gauge\n"+
			"mock_cpu_usage %f\n"+
			"# HELP mock_requests_total Total requests processed\n"+
			"# TYPE mock_requests_total counter\n"+
			"mock_requests_total %d\n"+
			"# HELP mock_memory_bytes Memory usage in bytes\n"+
			"# TYPE mock_memory_bytes gauge\n"+
			"mock_memory_bytes %d\n",
		rand.Float64()*100,
		rand.Intn(10000),
		rand.Int63n(1024*1024*1024),
	)
	if err != nil {
		log.Printf("write error: %v", err)
	}
}
