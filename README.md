# Log Normalizer Operator

Kubernetes operator that auto-injects a log-normalizing sidecar into pods. The sidecar detects log formats, reassembles multiline entries, and emits structured OTel-compatible JSON — without requiring application code changes or pre-configured parsing rules.

## Problem

Go applications in Kubernetes emit logs from multiple sources (application code, dependency libraries, panic traces) in inconsistent formats. Existing collection tools (Vector, OTel Collector, Alloy) require manually authored regex/VRL rules to parse and structure these logs. When a dependency starts logging in an unexpected format, the rules miss it. Multiline reassembly (stack traces, pretty-printed JSON) is especially brittle.

## How It Works

1. **Operator** deploys a mutating admission webhook
2. **Webhook** auto-injects a normalizer sidecar into pods in labeled namespaces
3. **Sidecar** intercepts stdout/stderr, auto-detects format, normalizes to structured JSON

The sidecar uses the Drain template mining algorithm to group structurally similar log lines into patterns, replacing variable parts (timestamps, IDs, paths) with wildcards. This enables automatic multiline boundary detection and template-based log classification — no regex required.

### Supported Formats

| Format | Detection | Example |
|--------|-----------|---------|
| JSON | Starts with `{`, ends with `}` | `{"level":"info","msg":"request completed"}` |
| klog | Starts with `[IWEF]` + 4 digits | `I0722 15:04:05.000000 12345 main.go:42] message` |
| Zap console | Tab-separated with level field | `2026-07-23T12:07:56Z\tINFO\tcontroller\tStarting` |
| logfmt | Contains 2+ `key=value` pairs | `level=info msg="hello" ts=2024-01-01` |
| Plain text | Everything else | `Processing batch 42 of 100` |

Zap console detection accepts both uppercase (`INFO`) and lowercase (`info`) levels, covering both controller-runtime operators and OTel Collector logs.

### Multiline Consolidators

| Language | Detects |
|----------|---------|
| Go | `panic:` blocks, goroutine dumps |
| Java | Stack traces with `\tat`, `Caused by:` |
| Python | `Traceback` blocks with `File "..."` |

### OTLP Export and Trace Correlation

When `spec.otlpEndpoint` is set in the `LogNormalizerConfig`, the sidecar dual-exports normalized logs: to container stdout (for `kubectl logs`) and via OTLP gRPC to an OTel Collector. Each log record includes:

- **Kubernetes resource attributes** (`k8s.pod.name`, `k8s.namespace.name`, `k8s.node.name`, `k8s.pod.uid`) injected via the Downward API
- **Trace/span IDs** extracted from log content (supports `trace_id`, `traceID`, `otel.trace_id`, `dd.trace_id`, and other common field names) and embedded in the OTLP `SpanContext`

The normalizer also filters out OTel Collector debug exporter feedback to prevent loops when running inside collector pods.

### Image ENTRYPOINT Resolution

The webhook automatically resolves image ENTRYPOINTs from container registries for containers without an explicit `command` in the pod spec. This enables wrapping all containers — including those relying on Dockerfile `ENTRYPOINT`/`CMD` — without manual configuration. Authentication uses pod `imagePullSecrets` and the cluster-wide pull secret.

## Quick Start

### Prerequisites

- Kubernetes 1.33+ or OpenShift 4.18+ (for native sidecar support via `restartPolicy: Always` init containers)
- `kubectl` or `oc` CLI

### Install

```bash
# Deploy operator (kustomize)
make deploy

# Or install via OLM — see docs/setup.md for full instructions
```

### Configure

Label namespaces where you want sidecar injection:

```bash
kubectl label namespace my-app lognormalizer.opendatahub.io/inject=enabled
```

Create a `LogNormalizerConfig`:

```bash
kubectl apply -f config/samples/lognormalizerconfig_default.yaml
```

New pods in labeled namespaces will automatically get the normalizer sidecar.

### Opt Out

Add an annotation to skip injection for specific pods:

```yaml
metadata:
  annotations:
    lognormalizer.opendatahub.io/inject: "false"
```

### Test Locally (CLI Mode)

The normalizer binary can process logs from stdin:

```bash
make build-normalizer
cat your-logs.txt | bin/normalizer
```

## Development

### Build

```bash
make build           # Build both binaries
make docker-build    # Build Docker images
make docker-push     # Push to registry
```

### Test

```bash
make test            # Unit tests with race detector
make e2e-test        # E2E tests against live cluster
make vet             # Compilation check
make fmt             # Format code
```

### Deploy Test Workloads

```bash
kubectl apply -f config/test/namespace.yaml
kubectl apply -f config/test/
```

### Project Structure

```
cmd/operator/        Operator binary (controller-runtime)
cmd/normalizer/      Sidecar binary (log interception + parsing + OTLP export)
cmd/wrapper/         Wrapper binary (tees stdout/stderr to named pipe)
api/v1alpha1/        CRD types (LogNormalizerConfig)
internal/controller/ Reconciler (manages webhook configuration)
internal/webhook/    Mutating webhook (sidecar injection + image resolution)
internal/normalizer/ Core: format detection, Drain miner, emitter, OTLP
  multiline/         Multiline reassembly + language consolidators
config/              Kubernetes manifests (CRD, RBAC, webhook, samples)
test/                E2E and integration tests
deploy/olm/          OLM bundle (CSV, CRD)
```

## Configuration Reference

See `config/samples/lognormalizerconfig_default.yaml` for the full CR spec with defaults. Key fields:

- `spec.namespaceSelector` — which namespaces get injection (label selector)
- `spec.sidecarImage` — normalizer container image
- `spec.otlpEndpoint` — OTel Collector gRPC endpoint for dual log export (optional)
- `spec.drain.similarityThreshold` — Drain algorithm threshold (0.0-1.0, default 0.4)
- `spec.multiline.enabled` — enable multiline reassembly
- `spec.consolidators` — language consolidators to enable (`go`, `java`, `python`)

## License

Apache License 2.0
