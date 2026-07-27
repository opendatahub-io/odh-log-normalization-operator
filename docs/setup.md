# Cluster Setup Guide

Deploy the log normalizer operator on a fresh Kubernetes or OpenShift cluster.

There are two installation methods:
- **[OLM Install](#olm-install)** — recommended for OpenShift clusters with OLM available
- **[Manual Install](#manual-install)** — works on any Kubernetes 1.33+ cluster

## Prerequisites

- Kubernetes 1.33+ or OpenShift 4.18+ (native sidecar containers require `restartPolicy: Always` on init containers)
- `kubectl` / `oc` CLI authenticated to the target cluster
- `podman` or `docker` for building images
- A container registry accessible from the cluster (internal OpenShift registry, quay.io, etc.)

---

## OLM Install

OLM handles CRD installation, RBAC, deployment, webhook TLS, and upgrades automatically.

### 1. Build and Push Images

Build the operator, normalizer, bundle, and catalog images:

```bash
export REGISTRY=quay.io/yourorg
# sign into the registry
podman login --tls-verify=false -u $(oc whoami) -p $(oc whoami -t) \
  $(oc get route default-route -n openshift-image-registry -o jsonpath='{.spec.host}')

# Build operator and normalizer images
podman build --platform linux/amd64 -t ${REGISTRY}/log-normalizer-operator:latest -f Dockerfile.operator .
podman build --platform linux/amd64 -t ${REGISTRY}/log-normalizer:latest -f Dockerfile.normalizer .

# Push
podman push ${REGISTRY}/log-normalizer-operator:latest
podman push ${REGISTRY}/log-normalizer:latest

# Build the OLM bundle (renders operator + normalizer images into the CSV)
make bundle IMG_OPERATOR=${REGISTRY}/log-normalizer-operator:latest \
            IMG_NORMALIZER=${REGISTRY}/log-normalizer:latest
podman build --platform linux/amd64 -f bundle.Dockerfile -t ${REGISTRY}/log-normalizer-operator-bundle:v0.1.0 .
podman push ${REGISTRY}/log-normalizer-operator-bundle:v0.1.0

# Build and push the catalog index (required — CatalogSource needs a catalog image, not a bundle)
make catalog-build CATALOG_IMG=${REGISTRY}/log-normalizer-operator-catalog:v0.1.0 \
                   BUNDLE_IMG=${REGISTRY}/log-normalizer-operator-bundle:v0.1.0
podman push ${REGISTRY}/log-normalizer-operator-catalog:v0.1.0
```

If using the OpenShift internal registry, create the namespace and image streams before pushing:

```bash
oc new-project log-normalizer-system
for img in log-normalizer-operator log-normalizer log-normalizer-operator-bundle log-normalizer-operator-catalog; do
  oc create imagestream $img -n log-normalizer-system
done

# Grant pull access to openshift-marketplace (for CatalogSource) and openshift-operators (OLM may deploy there)
oc policy add-role-to-group system:image-puller \
  system:serviceaccounts:openshift-marketplace -n log-normalizer-system
oc policy add-role-to-group system:image-puller \
  system:serviceaccounts:openshift-operators -n log-normalizer-system
```

When using the internal registry, you need **two** registry variables:

```bash
# External route — for podman push from your laptop
export EXT_REGISTRY=$(oc get route default-route -n openshift-image-registry \
  -o jsonpath='{.spec.host}')/log-normalizer-system

# Internal address — for manifests (CatalogSource, CSV, pod specs)
export INT_REGISTRY=image-registry.openshift-image-registry.svc:5000/log-normalizer-system
```

Use `EXT_REGISTRY` for `podman push`. Use `INT_REGISTRY` for `make bundle`, `make catalog-build`, and the CatalogSource `image:` field.

### 2. Create the CatalogSource

Point the CatalogSource at the **catalog** image using the **internal** registry address (pods inside the cluster cannot reach the external route):

```bash
cat <<EOF | kubectl apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: log-normalizer-catalog
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: ${INT_REGISTRY}/log-normalizer-operator-catalog:v0.1.0
  displayName: Log Normalizer Operator
  publisher: Red Hat
EOF
```

Verify the catalog is ready:

```bash
kubectl get catalogsource log-normalizer-catalog -n openshift-marketplace \
  -o jsonpath='{.status.connectionState.lastObservedState}'
# Expected: READY
```

### 3. Create Operator Namespace and Subscribe

OLM requires exactly one OperatorGroup per namespace. If the namespace already exists (e.g. from the image stream setup), skip the `create namespace` command.

```bash
kubectl create namespace log-normalizer-system

cat <<EOF | kubectl apply -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: log-normalizer-operator
  namespace: log-normalizer-system
spec: {}
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: log-normalizer-operator
  namespace: log-normalizer-system
spec:
  channel: alpha
  name: log-normalizer-operator
  source: log-normalizer-catalog
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
EOF
```

Wait for the operator to become ready:

```bash
kubectl get csv -n log-normalizer-system
# Expected: log-normalizer-operator.v0.1.0  Succeeded
```

### 4. Enable Injection and Verify

The operator automatically creates a default `LogNormalizerConfig` CR on startup with the correct `sidecarImage` (derived from `RELATED_IMAGE_NORMALIZER` in the deployment). No manual CR creation needed.

If using the OpenShift internal registry, grant image pull access from the target namespace:

```bash
oc policy add-role-to-group system:image-puller \
  system:serviceaccounts:<your-namespace> -n log-normalizer-system
```

Then label the namespace and restart workloads:

```bash
kubectl label namespace <your-namespace> lognormalizer.opendatahub.io/inject=enabled
kubectl rollout restart deployment,statefulset -n <your-namespace>
kubectl logs <pod-name> -c log-normalizer -n <your-namespace>
```

### 5. Configure OTLP Export (Optional)

To forward normalized logs to an OpenTelemetry Collector via OTLP gRPC, enable it on the `LogNormalizerConfig`:

```bash
kubectl patch lognormalizerconfig default --type=merge \
  -p '{"spec":{"otlpEndpoint":"data-science-collector-collector.opendatahub.svc:4317"}}'
kubectl rollout restart deployment,statefulset -n <your-namespace>
```

The receiving OTel Collector must have a `logs` pipeline configured with an OTLP receiver. If you're using the ODH monitoring stack, this requires a one-line change to the collector template in the `opendatahub-operator` repo:

**File:** `internal/controller/services/monitoring/resources/opentelemetry-collector.tmpl.yaml`

```yaml
exporters:
  debug:
    verbosity: basic      # <-- add debug exporter
  # ... existing exporters ...

service:
  pipelines:
    logs:                  # <-- add logs pipeline
      receivers: [otlp]
      processors: [memory_limiter, k8sattributes, resourcedetection, batch]
      exporters: [debug]
    # ... existing traces/metrics pipelines ...
```

Verify the collector is receiving logs:

```bash
kubectl logs <collector-pod> -c otc-container -n opendatahub | grep 'otelcol.signal.*logs'
# Expected: info  Logs  {"otelcol.component.id": "debug", ... "log records": N}
```

### OLM Uninstall

```bash
# Remove injection label and restart workloads to strip sidecars from existing pods
kubectl label namespace <your-namespace> lognormalizer.opendatahub.io/inject-
kubectl rollout restart deployment,statefulset -n <your-namespace>

# Remove OLM resources
kubectl delete subscription log-normalizer-operator -n log-normalizer-system
kubectl delete csv log-normalizer-operator.v0.1.0 -n log-normalizer-system
kubectl delete catalogsource log-normalizer-catalog -n openshift-marketplace
kubectl delete namespace log-normalizer-system
```

---

## Manual Install

For clusters without OLM, or when you need full control over each component.

### 1. Build and Push Images

```bash
# Set your registry (examples below)
export REGISTRY=quay.io/yourorg
# or for OpenShift internal registry:
# export REGISTRY=$(oc get route default-route -n openshift-image-registry -o jsonpath='{.spec.host}')/log-normalizer-system

# Build both images
podman build --platform linux/amd64 -t ${REGISTRY}/log-normalizer:latest -f Dockerfile.normalizer .
podman build --platform linux/amd64 -t ${REGISTRY}/log-normalizer-operator:latest -f Dockerfile.operator .

# Login & Push
podman login --tls-verify=false -u $(oc whoami) -p $(oc whoami -t) ${REGISTRY}
podman push ${REGISTRY}/log-normalizer:latest
podman push ${REGISTRY}/log-normalizer-operator:latest
```

If using the OpenShift internal registry, create the target namespace first (`oc new-project log-normalizer-system`) and grant pull access from other namespaces:

```bash
oc policy add-role-to-group system:image-puller \
  system:serviceaccounts:<target-namespace> -n log-normalizer-system
```

### 2. Generate and Install CRDs

```bash
make generate
kubectl apply -f config/crd/bases/lognormalizer.opendatahub.io_lognormalizerconfigs.yaml
```

### 3. Create Namespace and RBAC

```bash
kubectl apply -f config/default/namespace.yaml
kubectl apply -f config/rbac/role.yaml \
              -f config/rbac/role_binding.yaml \
              -f config/rbac/service_account.yaml
```

### 4. Generate Webhook TLS Certificates

The webhook server needs TLS. Choose one of:

#### Option A: cert-manager (recommended for production)

Install the [cert-manager operator](https://cert-manager.io/docs/installation/), then apply the certificate resources:

```bash
kubectl apply -f config/webhook/certificate.yaml
```

cert-manager will create the `log-normalizer-webhook-tls` secret automatically. The operator controller will pick up the CA from it.

#### Option B: Self-signed certificates (no cert-manager)

```bash
# Generate CA + server cert
openssl genrsa -out ca.key 2048
openssl req -x509 -new -key ca.key -sha256 -days 365 -out ca.crt \
  -subj "/CN=log-normalizer-webhook-ca"

openssl genrsa -out tls.key 2048
openssl req -new -key tls.key -out tls.csr \
  -subj "/CN=log-normalizer-webhook.log-normalizer-system.svc" \
  -addext "subjectAltName=DNS:log-normalizer-webhook.log-normalizer-system.svc,DNS:log-normalizer-webhook.log-normalizer-system.svc.cluster.local"

openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out tls.crt -days 365 -sha256 \
  -extfile <(echo "subjectAltName=DNS:log-normalizer-webhook.log-normalizer-system.svc,DNS:log-normalizer-webhook.log-normalizer-system.svc.cluster.local")

# Create the TLS secret (include ca.crt so the operator can read it)
kubectl create secret generic log-normalizer-webhook-tls \
  --from-file=tls.crt --from-file=tls.key --from-file=ca.crt \
  -n log-normalizer-system
```

#### Option C: Auto-generated (operator bootstraps its own certs)

If no TLS secret exists when the operator starts, it automatically generates a self-signed CA and server certificate. No manual step needed — skip to the next section.

### 5. Deploy the Operator

The operator auto-creates the webhook `Service` and `MutatingWebhookConfiguration` on startup.

```bash
# Operator deployment — update image references
OPERATOR_IMAGE=<your-registry>/log-normalizer-operator:latest
NORMALIZER_IMAGE=<your-registry>/log-normalizer:latest
sed -e "s|image: operator:latest|image: ${OPERATOR_IMAGE}|" \
    -e "s|value: normalizer:latest|value: ${NORMALIZER_IMAGE}|" \
    config/default/manager.yaml | kubectl apply -f -
```

The operator will:
1. Acquire the leader lease
2. Auto-create a default `LogNormalizerConfig` (using `RELATED_IMAGE_NORMALIZER` for the sidecar image)
3. Create the webhook `Service` (port 443 -> 9443)
4. Create/update the `MutatingWebhookConfiguration` with the CA bundle from the TLS secret

Verify:

```bash
kubectl get pods -n log-normalizer-system
# Expected: log-normalizer-operator pod 1/1 Running

kubectl logs -l app=log-normalizer-operator -n log-normalizer-system | grep "reconciled webhook"
# Expected: "reconciled webhook" {"result": "updated"}
```

### 6. Enable Injection on Target Namespaces

If using the OpenShift internal registry, grant image pull access from the target namespace:

```bash
oc policy add-role-to-group system:image-puller \
  system:serviceaccounts:<your-namespace> -n log-normalizer-system
```

Then label and restart:

```bash
kubectl label namespace <your-namespace> lognormalizer.opendatahub.io/inject=enabled
```

Existing pods are not affected — only new pods created after labeling get the sidecar. To inject into existing workloads, restart their deployments:

```bash
kubectl rollout restart deployment/<name> -n <your-namespace>
```

### 7. Verify

```bash
# Check the pod has the sidecar
kubectl get pod <pod-name> -n <your-namespace> -o jsonpath='{.spec.initContainers[*].name}'
# Expected: log-normalizer-copy-wrapper log-normalizer

# View normalized logs
kubectl logs <pod-name> -c log-normalizer -n <your-namespace>
```

Each normalized log line includes structured fields:

```json
{
  "timestamp": "2026-07-23T15:33:12.236Z",
  "level": "info",
  "message": "Starting otelcol...",
  "source_format": "controller-runtime",
  "template_id": 5,
  "resource": {
    "k8s.pod.name": "my-pod",
    "k8s.namespace.name": "opendatahub"
  },
  "structured": {"Version": "0.152.1"}
}
```

To enable OTLP export and configure the OTel Collector logs pipeline, see [Configure OTLP Export](#5-configure-otlp-export-optional) in the OLM section — the same steps apply for manual installs.

### Limitations

- **The sidecar image must be pullable** from every namespace where injection is enabled. When using the OpenShift internal registry, grant `system:image-puller` per namespace (see step 1).
- **Image ENTRYPOINT resolution** requires network access from the webhook to container registries. If a registry is unreachable, the container's command won't be wrapped but the pod will still be admitted (resolution errors are non-fatal).

### Manual Uninstall

```bash
# Remove injection label and restart workloads to strip sidecars from existing pods
kubectl label namespace <your-namespace> lognormalizer.opendatahub.io/inject-
kubectl rollout restart deployment,statefulset -n <your-namespace>

# Remove operator and resources
kubectl delete -f config/default/manager.yaml
kubectl delete -f config/rbac/role.yaml \
              -f config/rbac/role_binding.yaml \
              -f config/rbac/service_account.yaml
kubectl delete -f config/crd/bases/
kubectl delete namespace log-normalizer-system
```
