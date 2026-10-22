##@ Development

BINARY := toolchain-manager

.PHONY: build-linux-amd64
build-linux-amd64: ## Build the linux/amd64 binary the Dockerfile expects.
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(BINARY)-linux-amd64 .

.PHONY: docker-build
docker-build: build-linux-amd64 ## Build a local dev image (TAG=toolchain-manager:dev).
	docker build --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t $(or $(TAG),toolchain-manager:dev) .

##@ API

CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0
CRD_DIR := helm/toolchain-manager/files/crds

.PHONY: generate
generate: ## Generate the API's deepcopy functions and the CRD the chart ships from api/.
	$(CONTROLLER_GEN) object paths=./api/...
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=$(CRD_DIR)

.PHONY: verify-generate
verify-generate: generate ## Fail when the generated API files differ from api/.
	git diff --exit-code -- api $(CRD_DIR)

##@ Helm

.PHONY: helm-unittest
helm-unittest: ## Run the helm-unittest suites in helm/toolchain-manager/tests/.
	helm unittest helm/toolchain-manager
