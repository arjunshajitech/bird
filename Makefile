# Bird — build, test and (opt-in) install.
#
#   make              build both binaries into ./build
#   make test         run unit tests (race detector on)
#   make install      copy binaries to ~/.local/bin
#   make install-service   install + enable the systemd --user unit
#
# Nothing is installed or enabled unless you run an install target yourself.

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PREFIX    ?= $(HOME)/.local
BINDIR    ?= $(PREFIX)/bin
CONFIGDIR ?= $(or $(XDG_CONFIG_HOME),$(HOME)/.config)/bird
UNITDIR   ?= $(or $(XDG_CONFIG_HOME),$(HOME)/.config)/systemd/user
BUILD     := build

# WebKitGTK 4.1 is current on Ubuntu 24.04+/Debian 13+/Fedora. Set
# WEBKIT_TAG= (empty) to build against the older webkit2gtk-4.0.
WEBKIT_TAG ?= webkit2_41
comma := ,
OVERLAY_TAGS := desktop$(comma)production$(if $(WEBKIT_TAG),$(comma)$(WEBKIT_TAG))

LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all bird overlay test fmt vet check-deps install install-config install-service \
        uninstall uninstall-service clean

all: bird overlay

# The daemon is pure Go: no cgo, no GUI libraries.
bird:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BUILD)/bird ./cmd/bird

# The overlay links GTK3 + WebKitGTK through Wails (cgo).
overlay: check-deps
	go build -trimpath -tags $(OVERLAY_TAGS) -ldflags '$(LDFLAGS)' -o $(BUILD)/bird-overlay ./cmd/bird-overlay

check-deps:
	@pkg-config --exists gtk+-3.0 $(if $(WEBKIT_TAG),webkit2gtk-4.1,webkit2gtk-4.0) || { \
	  echo "Missing GTK/WebKitGTK development files. On Debian/Ubuntu:"; \
	  echo "  sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev"; \
	  echo "On Fedora:  sudo dnf install gcc pkgconf gtk3-devel webkit2gtk4.1-devel"; \
	  echo "On Arch:    sudo pacman -S base-devel gtk3 webkit2gtk-4.1"; \
	  exit 1; }

test:
	go test -race ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

install: all
	install -d $(BINDIR)
	install -m 0755 $(BUILD)/bird $(BUILD)/bird-overlay $(BINDIR)/
	@echo "Installed to $(BINDIR). Next: make install-config (if new), then make install-service."

# Never overwrites an existing configuration.
install-config:
	install -d -m 0700 $(CONFIGDIR)
	@if [ -e $(CONFIGDIR)/config.yaml ]; then \
	  echo "$(CONFIGDIR)/config.yaml exists; not overwriting."; \
	else \
	  install -m 0600 examples/workday.yaml $(CONFIGDIR)/config.yaml; \
	  echo "Created $(CONFIGDIR)/config.yaml — edit it, then: $(BINDIR)/bird --check-config"; \
	fi

install-service:
	install -d $(UNITDIR)
	sed 's#%h/.local/bin#$(BINDIR)#' packaging/systemd/bird.service > $(UNITDIR)/bird.service
	systemctl --user daemon-reload
	systemctl --user enable --now bird.service
	@echo "Bird is running. Logs: journalctl --user -u bird -f"

uninstall-service:
	-systemctl --user disable --now bird.service
	rm -f $(UNITDIR)/bird.service
	-systemctl --user daemon-reload

# Keeps your configuration and state; remove them by hand if you want:
#   rm -r ~/.config/bird ~/.local/state/bird ~/.cache/bird
uninstall: uninstall-service
	rm -f $(BINDIR)/bird $(BINDIR)/bird-overlay

clean:
	rm -rf $(BUILD)
