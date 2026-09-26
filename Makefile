PREFIX  ?= $(HOME)/.local
UNITDIR ?= $(HOME)/.config/systemd/user

.PHONY: build test install uninstall plugin unplugin samples screenshots

build:
	go build -o bin/claude-display .

test:
	go test ./...

# Install the daemon as a systemd user service and (re)start it.
install: build
	install -Dm755 bin/claude-display $(PREFIX)/bin/claude-display
	install -Dm644 deploy/claude-display.service $(UNITDIR)/claude-display.service
	systemctl --user daemon-reload
	systemctl --user enable claude-display
	systemctl --user restart claude-display

uninstall:
	-systemctl --user disable --now claude-display
	rm -f $(PREFIX)/bin/claude-display $(UNITDIR)/claude-display.service
	systemctl --user daemon-reload

# Register this repo as a local marketplace and install the hooks plugin.
# Claude Code reads the plugin in place, so hook edits apply to new sessions.
plugin:
	claude plugin marketplace add $(CURDIR)
	claude plugin install claude-display@weact-usb-display-claude-status

unplugin:
	claude plugin marketplace remove weact-usb-display-claude-status

# Render every screen to PNG for layout review without the panel.
samples: build
	bin/claude-display -samples samples

# The README gallery: every screen at 3x, pixel for pixel.
screenshots: build
	bin/claude-display -samples docs/screens -scale 3
