.PHONY: all build test check-design check-spec build-go test-go lint-go

VER = $(shell git branch --show-current | tr '/' '-')
IMAGE = talebook/talebook:$(VER)
REPO1 := talebook/talebook:latest
REPO2 := talebook/calibre-webserver:latest
GHCR_REPO1 := ghcr.io/talebook/talebook:latest
GHCR_DEV := ghcr.io/talebook/talebook:dev
TAG1 := talebook/talebook:server-side-render
TAG2 = talebook/talebook:server-side-render-$(VER)

all: build up

init:
	pip3 install -r requirements.txt pytest
	#python3 -m pip install --upgrade pip
	#uv sync

build: test build-spa build-ssr

build-spa:
	docker build --no-cache=false --build-arg BUILD_COUNTRY=CN --build-arg GIT_VERSION=$(VER) \
		-f Dockerfile -t $(IMAGE) -t $(REPO1) -t $(GHCR_REPO1) --target production -t $(REPO2) .

build-ssr:
	docker build --no-cache=false --build-arg BUILD_COUNTRY=CN --build-arg GIT_VERSION=$(VER) \
		-f Dockerfile -t $(TAG1) -t $(TAG2) --target production-ssr .

build-dev:
	docker build --no-cache=false --build-arg BUILD_COUNTRY=CN \
		-f Dockerfile -t talebook/talebook:dev -t $(GHCR_DEV) --target dev .

test:
	rm -f unittest.log
	docker build --build-arg BUILD_COUNTRY=CN -t talebook/test --target test -f Dockerfile .
	docker run --rm --name=talebook-docker-test -v "$$PWD":"$$PWD" -w "$$PWD/booksvr" talebook/test go test ./...

lint-ui:
	npm ci
	cd app && npm run lint

lint-py:
	ruff check ./docker ./scripts/upgrade --no-cache
	ruff format --diff ./docker ./scripts/upgrade --output-format concise --no-cache

lint-py-fix:
	ruff check ./docker ./scripts/upgrade --fix
	ruff format ./docker ./scripts/upgrade

check-i18n:
	uv run check_i18n_translation_missing.py
	uv run check_i18n_translation_useless.py

check-design:
	python3 scripts/check_design_docs.py

check-spec:
	python3 scripts/check_spec.py

build-go:
	cd booksvr && go build -o bin/booksvr ./cmd/booksvr

test-go:
	cd booksvr && go test ./...

lint-go:
	cd booksvr && go vet ./...

pytest:
	cd booksvr && go test ./...

testv:
	cd booksvr && go test -coverprofile=coverage.out ./...

testvv: testv
	cd booksvr && go tool cover -html=coverage.out

up:
	docker compose up

down:
	docker compose stop

dev: build-dev
	docker-compose -f docker-compose.dev.yml up

dev/ui:
	cd app && npm run dev
