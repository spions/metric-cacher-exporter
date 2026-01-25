# Metric Cacher Exporter

`metric-cacher-exporter` is a proxy service for caching Prometheus-format metrics.
It is designed to reduce the load on monitoring systems and services by caching duplicate requests in Redis.

## Use Case

Sharding or replication is typically used to ensure the fault tolerance of a zonal VMStack.

VMAgent's sharded mode does not provide full fault tolerance because if one shard (pod) in a zone fails, the data from the targets processed by that shard will be lost.

Switching VMAgent to replication mode — for example, using 2 or more replicas (usually matching the number of availability zones) — leads to:
- Increased requests to the metrics source, which may have request limits.
- Increased maintenance costs if metrics are paid/metered.
- Potential issues with graphs where scrape time offsets are not accounted for.

`metric-cacher-exporter` solves these problems.
Confirmed savings amounted to approximately 70-80% of the total cost of the metric collection service.


## Key Features

- **Metric Caching**: Saves responses from upstream services in Redis.
- **Flexible TTL Configuration**: Cache lifetime is set for each request via the `cacheTTL` parameter.
- **Request Deduplication**: If multiple requests for the same data arrive simultaneously, only one request is sent upstream, while the others wait for the result.
- **Simple Integration**: When using VictoriaMetrics or Prometheus, you only need to add a few lines to `relabel_configs` without changing the main query.


## Installation and Usage

By default, `metric-cacher-exporter` listens on HTTP port 8080.

```Bash
docker run -d \
  --name metric-cacher-exporter \
  -p 8080:8080 \
  -e REDIS_ADDR=redis:6379 \
  spions/metric-cacher-exporter:latest
```

## Usage

The service acts as a proxy. You pass the destination address (upstream) in the `target` parameter.

### Example Request:
```
http://localhost:8080/metrics?target=http://my-service:9090/metrics&cacheTTL=30
```

### Request Parameters:
- `target`: The URL or host address from which metrics should be collected.
- `cacheTTL`: (Optional) Cache lifetime in seconds. Default: 60.

## Configuration

The service is configured via command-line flags or environment variables.

| Flag | Environment Variable | Description | Default |
|------|----------------------|-------------|---------|
| `--redis.addr` | `REDIS_ADDR` | Redis server address | `localhost:6379` |
| `--redis.password` | `REDIS_PASSWORD` | Redis password | `""` |
| `--redis.db` | `REDIS_DB` | Redis database number | `0` |
| `--web.listen-address` | (none) | Address and port the service will listen on | `:8080` |

## Service Metrics

The service provides its own metrics at the `/metrics` endpoint:

- `mce_http_requests_total`: Total number of requests (partitioned by `upstream`, `status`, and `cache_status`).
- `mce_cacher_exported_metrics_lines_total`: Total number of metric lines processed by the cacher.
- `mce_cache_error_total`: Counter of errors encountered during cache operations.

## Integration Example with VictoriaMetrics / Prometheus

In your scrape configuration, use `relabelConfigs` to redirect requests through the cacher:

```yaml VictoriaMetrics
spec:
  path: "/metrics"
  scrape_interval: 60s
  params:
    cacheTTL: 
      - "45"
  staticConfigs:
    - targets: ["my-service:9090"]
  relabelConfigs:
    - source_labels: [__address__]
      target_label: __param_target
    - source_labels: [__param_target]
      target_label: instance
    - target_label: __address__
      replacement: metric-cacher-exporter:8080
```