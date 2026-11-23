# win-phone-mode -- switch a Windows laptop into a headless phone mode and
# restore its exact prior state.

BIN     := win-phone-mode.exe
PREFIX  ?= $(LOCALAPPDATA)/Programs/win-phone-mode

.PHONY: all build test vet check install uninstall clean cross

all: build

build:
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(BIN) .

# win.ps1 must sit beside the binary: host_windows.go refuses to load it from
# the current directory, because it runs PowerShell with administrator rights.
install: build
	mkdir -p "$(PREFIX)"
	cp $(BIN) win.ps1 "$(PREFIX)/"
	@echo "installed to $(PREFIX)"
	@echo "add that directory to PATH, then run: win-phone-mode on --dry-run"

uninstall:
	rm -rf "$(PREFIX)"

test:
	go test ./...

vet:
	go vet ./...
	GOOS=windows go vet ./...

check: vet test

# Cross-compile on any OS to eyeball the Windows artifact.
cross:
	GOOS=windows GOARCH=amd64 go build -trimpath -o $(BIN) .
	GOOS=windows GOARCH=amd64 go build -trimpath -o win-phone-mode-arm64.exe .

clean:
	rm -f $(BIN) win-phone-mode-arm64.exe
