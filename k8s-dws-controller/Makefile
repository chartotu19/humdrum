IMG ?= dws-controller:latest

.PHONY: all run test lint docker-build docker-push install uninstall deploy undeploy

all: lint test

lint:
	python -m py_compile src/main.py
	python -m py_compile src/controller.py
	python -m py_compile src/queue_manager.py
	python -m py_compile src/models.py

run:
	python -m src.main --dev

test:
	python -m pytest tests/ -v --tb=short

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
