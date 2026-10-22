# Jinxed Linux agent - Adaptix extender
#
# `make` produces dist/ containing everything the teamserver needs:
#   dist/agent_jinxed_linux.so   Go plugin (extender)
#   dist/config.yaml             extender descriptor
#   dist/ax_config.axs           operator client extension
#   dist/src_agent/              agent source used by BuildPayload
#
# NOTE: Go plugins are only loadable by a teamserver built with the exact same
# Go toolchain (and GOEXPERIMENT set). Keep GOEXPERIMENT in sync with the
# AdaptixC2 build; set GOEXPERIMENT= to disable it.

GO            ?= go
GOEXPERIMENT  ?= jsonv2,greenteagc
PLUGIN        := agent_jinxed_linux.so
DIST          := dist

.PHONY: all clean plugin stage

all: clean plugin stage

plugin:
	@echo "    * building $(PLUGIN)"
	@mkdir -p $(DIST)
	@GOEXPERIMENT=$(GOEXPERIMENT) $(GO) build -buildmode=plugin \
		-ldflags="-s -w" -o $(DIST)/$(PLUGIN) .
	@echo "      done"

stage:
	@echo "    * staging config.yaml, ax_config.axs and agent source"
	@cp config.yaml $(DIST)/config.yaml
	@cp ../../client/jinxed_linux.axs $(DIST)/ax_config.axs
	@rm -rf $(DIST)/src_agent
	@cp -r ../agent $(DIST)/src_agent
	@rm -f $(DIST)/src_agent/jinxed_* $(DIST)/src_agent/agent $(DIST)/src_agent/*.test
	@echo "      done"
	@echo
	@echo "Add to the teamserver profile.yaml:"
	@echo "    extenders:"
	@echo "      - \"extenders/jinxed_linux/config.yaml\""

clean:
	@rm -rf $(DIST)

# Convenience: compile a standalone test agent straight from the source tree
# (it will refuse to run because it has no embedded profile).
check:
	@cd ../agent && CGO_ENABLED=0 GOOS=linux $(GO) build -o /dev/null .
	@cd ../agent && CGO_ENABLED=0 GOOS=linux $(GO) vet ./...
