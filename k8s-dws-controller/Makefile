IMG ?= dws-controller:latest

.PHONY: all build run test docker-build docker-push install uninstall deploy undeploy fmt vet

all: build

fmt:
	go fmt ./...

vet: fmt
	go vet ./...

build: vet
	go build -o bin/manager ./cmd/controller/

run: vet
	go run ./cmd/controller/

test:
	go test ./... -coverprofile cover.out

docker-build:
	docker build -t $(IMG) .

docker-push:
	docker push $(IMG)

# Install CRDs into a cluster.
install:
	kubectl apply -f config/crd/

# Uninstall CRDs from a cluster.
uninstall:
	kubectl delete -f config/crd/

# Deploy controller to the cluster.
deploy: install
	kubectl apply -f config/rbac/
	kubectl apply -f config/runtimeclass/
	kubectl apply -f config/manager/

# Undeploy controller from the cluster.
undeploy:
	kubectl delete -f config/manager/ || true
	kubectl delete -f config/runtimeclass/ || true
	kubectl delete -f config/rbac/ || true
	kubectl delete -f config/crd/ || true
