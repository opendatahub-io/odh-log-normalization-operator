IMG_OPERATOR ?= quay.io/opendatahub/log-normalizer-operator:latest
IMG_NORMALIZER ?= quay.io/opendatahub/log-normalizer:latest
BUNDLE_IMG ?= quay.io/opendatahub/log-normalizer-operator-bundle:v0.1.0
CATALOG_IMG ?= quay.io/opendatahub/log-normalizer-operator-catalog:v0.1.0
VERSION ?= 0.1.0
NAMESPACE ?= log-normalizer-system
CONTAINER_TOOL ?= podman

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: build-operator build-normalizer ## Build both binaries

.PHONY: build-operator
build-operator: ## Build the operator binary
	go build -o bin/operator ./cmd/operator/

.PHONY: build-normalizer
build-normalizer: ## Build the normalizer sidecar binary
	go build -o bin/normalizer ./cmd/normalizer/

.PHONY: test
test: ## Run unit and integration tests
	go test -race -count=1 ./internal/... ./api/... ./test/integration/...

.PHONY: bench
bench: ## Run benchmarks
	go test -bench=. -benchmem -run=^$$ ./internal/normalizer/

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format code
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: tidy
tidy: ## Run go mod tidy
	go mod tidy

.PHONY: generate
generate: ## Generate DeepCopy methods and CRD manifests
	controller-gen object paths="./api/..." output:object:dir=./api/v1alpha1
	controller-gen crd:allowDangerousTypes=true paths="./api/..." output:crd:dir=./config/crd/bases
	controller-gen rbac:roleName=log-normalizer-operator paths="./internal/..." output:rbac:dir=./config/rbac

.PHONY: docker-build
docker-build: docker-build-operator docker-build-normalizer ## Build both Docker images

.PHONY: docker-build-operator
docker-build-operator: ## Build the operator Docker image
	docker build -t $(IMG_OPERATOR) -f Dockerfile.operator .

.PHONY: docker-build-normalizer
docker-build-normalizer: ## Build the normalizer sidecar Docker image
	docker build -t $(IMG_NORMALIZER) -f Dockerfile.normalizer .

.PHONY: docker-push
docker-push: ## Push both Docker images
	docker push $(IMG_OPERATOR)
	docker push $(IMG_NORMALIZER)

.PHONY: deploy
deploy: ## Deploy operator to the cluster via kustomize
	cd config/default && kustomize edit set image operator=$(IMG_OPERATOR)
	kustomize build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: ## Remove operator from the cluster
	kustomize build config/default | kubectl delete --ignore-not-found -f -

.PHONY: install
install: ## Install CRDs into the cluster
	kubectl apply -f config/crd/bases/

.PHONY: uninstall
uninstall: ## Remove CRDs from the cluster
	kubectl delete --ignore-not-found -f config/crd/bases/

.PHONY: deploy-sample
deploy-sample: ## Deploy a sample LogNormalizerConfig CR
	kubectl apply -f config/samples/

.PHONY: deploy-test-workloads
deploy-test-workloads: ## Deploy test workloads to the cluster
	kubectl apply -f config/test/

.PHONY: e2e-test
e2e-test: ## Run E2E tests against the connected cluster
	go test -v -timeout 30m ./test/e2e/ -count=1

.PHONY: bundle
bundle: generate ## Generate OLM bundle manifests
	cp config/crd/bases/lognormalizer.opendatahub.io_lognormalizerconfigs.yaml bundle/manifests/
	sed -e "s|OPERATOR_IMG_PLACEHOLDER|$(IMG_OPERATOR)|" \
	    -e "s|NORMALIZER_IMG_PLACEHOLDER|$(IMG_NORMALIZER)|" \
	    deploy/olm/log-normalizer-operator.clusterserviceversion.yaml > bundle/manifests/log-normalizer-operator.clusterserviceversion.yaml

.PHONY: bundle-build
bundle-build: bundle ## Build the OLM bundle image
	docker build -f bundle.Dockerfile -t $(BUNDLE_IMG) .

.PHONY: bundle-push
bundle-push: ## Push the OLM bundle image
	docker push $(BUNDLE_IMG)

.PHONY: catalog-build
catalog-build: ## Build a file-based catalog image for OLM
	sed "s|BUNDLE_IMG_PLACEHOLDER|$(BUNDLE_IMG)|" catalog/index.yaml > catalog/index.rendered.yaml
	$(CONTAINER_TOOL) build --platform linux/amd64 -f catalog.Dockerfile -t $(CATALOG_IMG) . && rm -f catalog/index.rendered.yaml

.PHONY: catalog-push
catalog-push: ## Push the OLM catalog image
	$(CONTAINER_TOOL) push --tls-verify=false $(CATALOG_IMG)

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/
