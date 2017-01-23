COMMIT     := $(shell git rev-parse --short HEAD)
VERSION    := 1.1

LDFLAGS    := -ldflags \
              "-X main.Commit=$(COMMIT)\
               -X main.Version=$(VERSION)"

GOOS       := $(shell go env GOOS)
GOARCH     := $(shell go env GOARCH)
GORUN      := GOOS=$(GOOS) GOARCH=$(GOARCH) go run *.go
GOBUILD    := GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS)

ARCHIVE    := rqmon-$(VERSION)-$(GOOS)-$(GOARCH).tar.gz
DISTDIR    := dist/$(GOOS)_$(GOARCH)

.PHONY: default deps run release archive clean

default: *.go
	$(GOBUILD)

deps:
	go get -u github.com/garyburd/redigo/redis

run: *.go
	$(GORUN)

release: REMOTE     ?= $(error "can't release, REMOTE not set")
release: REMOTE_DIR ?= $(error "can't release, REMOTE_DIR not set")
release: dist/$(ARCHIVE)
	scp $< $(REMOTE):$(REMOTE_DIR)/

archive: dist/$(ARCHIVE)

clean:
	git clean -f -x -d

dist/$(ARCHIVE): $(DISTDIR)/rqmon
	tar -C $(DISTDIR) -czvf $@ .

$(DISTDIR)/rqmon: *.go
	$(GOBUILD) -o $@
