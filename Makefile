# CMD_CLANG ?= $(shell brew --prefix llvm)/bin/clang
CMD_CLANG ?= clang
CMD_GO ?= go
CMD_RM ?= rm
BUILD_PATH ?= ./build

HOST_OS := $(shell uname -s)
HOST_ARCH := $(shell uname -m)

ifeq ($(HOST_OS),Darwin)
DOCKER_PLATFORM ?= linux/arm64
CMD_BPFTOOL ?= docker run --rm --platform $(DOCKER_PLATFORM) -v $(CURDIR):/src -w /src alpine:3.20 /src/assets/bpftool

ifneq ($(wildcard $(NDK_ROOT)/toolchains/llvm/prebuilt/darwin-arm64),)
NDK_HOST_TAG ?= darwin-arm64
else
NDK_HOST_TAG ?= darwin-x86_64
endif

TOOLCHAIN_BIN ?= $(NDK_ROOT)/toolchains/llvm/prebuilt/$(NDK_HOST_TAG)/bin
ANDROID_CC ?= $(TOOLCHAIN_BIN)/aarch64-linux-android29-clang
ANDROID_CXX ?= $(TOOLCHAIN_BIN)/aarch64-linux-android29-clang++
else
CMD_BPFTOOL ?= ./assets/bpftool
ANDROID_CC ?= aarch64-linux-android29-clang
ANDROID_CXX ?= aarch64-linux-android29-clang++
endif

DEBUG_PRINT ?=
LINUX_ARCH = arm64
ifeq ($(DEBUG),1)
DEBUG_PRINT := -DDEBUG_PRINT
endif

BUILD_TAGS ?=
TARGET_ARCH = $(LINUX_ARCH)

ifeq ($(BUILD_TAGS),forarm)
BUILD_TAGS := -tags forarm
TARGET_ARCH = arm
endif

.PHONY: all
all: ebpf_module genbtf assets build 
	@echo $(shell date)

.PHONY: clean
clean:
	$(CMD_RM) -f assets/*.d
	$(CMD_RM) -f assets/*.o
	$(CMD_RM) -f assets/ebpf_probe.go
	$(CMD_RM) -f bin/eDBG_$(TARGET_ARCH)

.PHONY: ebpf_module
ebpf_module:
	$(CMD_CLANG) \
	-D__TARGET_ARCH_$(TARGET_ARCH) \
	--target=bpf \
	-c \
	-nostdlibinc \
	-no-canonical-prefixes \
	-O2 \
	-I       libbpf/src \
	-I       ebpf_module \
	-g \
	-o assets/ebpf_module.o \
	ebpf_module/ebpf_module.c

.PHONY: assets
assets:
	$(CMD_GO) run github.com/shuLhan/go-bindata/cmd/go-bindata -pkg assets -o "assets/ebpf_probe.go" $(wildcard ./config/config_syscall_*.json ./assets/*.o ./assets/*_min.btf ./preload_libs/*.so)

.PHONY: genbtf
genbtf:
	$(CMD_BPFTOOL) gen min_core_btf assets/rock5b-5.10-f9d1b1529-arm64.btf assets/rock5b-5.10-arm64_min.btf assets/ebpf_module.o
	$(CMD_BPFTOOL) gen min_core_btf assets/a12-5.10-arm64.btf assets/a12-5.10-arm64_min.btf assets/ebpf_module.o

.PHONY: build
build:
	CGO_ENABLED=1 CC=$(ANDROID_CC) CXX=$(ANDROID_CXX) GOARCH=arm64 GOOS=android $(CMD_GO) build $(BUILD_TAGS) -ldflags "-w -s -extldflags '-Wl,--hash-style=sysv'" -o bin/eDBG_$(TARGET_ARCH) .
