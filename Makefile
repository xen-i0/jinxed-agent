# Jinxed Linux agent — top-level build entry points.
#
#   make            build the extender plugin and stage dist/
#   make agent      build a standalone amd64 agent (placeholder profile)
#   make test       run the full end-to-end protocol test
#   make check      vet + compile all packages
#   make clean      remove build artefacts

GO ?= go

.PHONY: all extender agent test check clean

all: extender

extender:
	@$(MAKE) -C src/extender all

agent:
	@mkdir -p build
	@cd src/agent && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build \
		-trimpath -buildvcs=false -ldflags="-s -w" -o ../../build/jinxed_amd64 .
	@echo "built build/jinxed_amd64 (placeholder profile: not a usable payload)"

test:
	@cd src/extender && $(GO) test -run TestEndToEnd -v -count=1 .

check:
	@cd src/agent && CGO_ENABLED=0 $(GO) vet ./... && CGO_ENABLED=0 $(GO) build -o /dev/null .
	@cd src/extender && $(GO) vet ./...

clean:
	@$(MAKE) -C src/extender clean
	@rm -rf build
