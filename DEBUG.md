# GPMS Chartplotter Debug Guide

This guide documents the verified development workflow for building and debugging the GPMS Chartplotter Go server with the embedded `tile57` Zig engine.

## 1. Expected repository layout

```text
/mnt/code/project/gpms/chartplotter/
├── .vscode/
│   └── launch.json
└── chartplotter/
    ├── bin/
    ├── cmd/
    │   └── chartplotter/
    ├── internal/
    ├── tile57/
    │   ├── bindings/go/
    │   ├── src/
    │   ├── build.zig
    │   └── zig-out/
    ├── web/
    ├── go.mod
    ├── go.sum
    └── Makefile
```

Important: `tile57` must be inside the Chartplotter repo as `chartplotter/tile57`. Do not keep a second manually copied tile57 for this project.

The root `go.mod` uses the local binding:

```go
github.com/beetlebugorg/tile57/bindings/go v0.0.0

replace github.com/beetlebugorg/tile57/bindings/go => ./tile57/bindings/go
```

So the Go linker expects the native library at:

```text
chartplotter/tile57/zig-out/lib/libtile57.a
```

## 2. Required tools

Verify:

```bash
go version
zig version
git --version
gcc --version
make --version
gdb --version
lldb --version
```

The current GPMS setup uses Go 1.26.x and Zig 0.16.0.

## 3. Initialize submodules

```bash
cd /mnt/code/project/gpms/chartplotter/chartplotter

git submodule update --init --recursive

git submodule status
ls -la tile57
```

`tile57` should contain files/directories such as:

```text
build.zig
bindings/
src/
```

## 4. Build tile57 in Debug mode

```bash
cd /mnt/code/project/gpms/chartplotter/chartplotter/tile57

rm -rf zig-out .zig-cache

zig build -Doptimize=Debug
zig build test -Doptimize=Debug
```

Check the test exit code:

```bash
echo $?
```

Expected:

```text
0
```

`0` means the test command passed.

## 5. Verify libtile57.a

```bash
ls -lh zig-out/lib/libtile57.a
file zig-out/lib/libtile57.a
```

Example successful output:

```text
-rw-rw-r-- 1 vergil vergil 96M ... zig-out/lib/libtile57.a
zig-out/lib/libtile57.a: current ar archive
```

At this point the native engine is ready.

## 6. Verify the local Go binding

Return to the Go project root:

```bash
cd ..

grep -n "tile57" go.mod
```

Expected lines are similar to:

```text
18: github.com/beetlebugorg/tile57/bindings/go v0.0.0
39:replace github.com/beetlebugorg/tile57/bindings/go => ./tile57/bindings/go
```

The resulting chain is:

```text
Chartplotter Go server
        ↓
./tile57/bindings/go
        ↓ CGO
./tile57/zig-out/lib/libtile57.a
        ↓
tile57 Zig engine
```

## 7. Inspect the normal Makefile build

```bash
make -n build
```

The important commands should include something similar to:

```text
cd "tile57" && zig build
```

and then:

```text
CGO_ENABLED=1 go build ... -o bin/chartplotter ./cmd/chartplotter
```

This confirms the normal build is designed to build tile57 first and then link it into the Go binary.

For debugging, use explicit debug flags instead of relying on the normal release build.

## 8. Build the Go debug binary

From:

```text
/mnt/code/project/gpms/chartplotter/chartplotter
```

run:

```bash
CGO_ENABLED=1 go build \
    -gcflags="all=-N -l" \
    -o bin/chartplotter-debug \
    ./cmd/chartplotter
```

The flags mean:

```text
CGO_ENABLED=1  Enable CGO so Go can link tile57
-N             Disable Go compiler optimizations
-l             Disable function inlining
```

The `-N -l` flags make source debugging and breakpoints reliable.

## 9. Verify the debug binary

```bash
ls -lh bin/chartplotter-debug
file bin/chartplotter-debug
```

A correct debug binary should show something similar to:

```text
ELF 64-bit LSB executable, x86-64, ... with debug_info, not stripped
```

The important part is:

```text
with debug_info, not stripped
```

## 10. Verify the Chartplotter CLI

```bash
./bin/chartplotter-debug --help
```

Expected commands include:

```text
version
emit-assets
catalog-json
bake
serve
simulate
plugin install
plugin list
plugin enable
plugin disable
plugin remove
plugin dev
```

The HTTP server is started with:

```bash
./bin/chartplotter-debug serve
```

Expected runtime output is similar to:

```text
tile57: emitted 6 S-101 client asset file(s) from libtile57 embedded catalogue
portrayal: S-101 (libtile57 embedded catalogue)
chartplotter -> http://127.0.0.1:8080/
```

Open:

```text
http://localhost:8080
```

If the map UI renders, the Go + tile57 runtime integration is working.

## 11. Install and verify Delve

Install:

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
```

Ensure the Go bin directory is in PATH:

```bash
export PATH="$HOME/go/bin:$PATH"
```

For a permanent setup:

```bash
echo 'export PATH="$HOME/go/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

Verify:

```bash
dlv version
```

Example:

```text
Delve Debugger
Version: 1.27.2
```

## 12. Test Delve manually

```bash
dlv exec ./bin/chartplotter-debug -- serve
```

Expected:

```text
Type 'help' for list of commands.
(dlv)
```

Useful commands:

```text
break main.main
continue
next
step
locals
args
print <variable>
goroutines
stack
quit
```

Example:

```text
(dlv) break main.main
(dlv) continue
```

If Delve stops at the breakpoint, terminal debugging is working.

## 13. Install the VS Code Go extension

Open VS Code Extensions:

```text
Ctrl + Shift + X
```

Install:

```text
Go
Publisher: Go Team at Google
Extension ID: golang.Go
```

Then run:

```text
Ctrl + Shift + P
Go: Install/Update Tools
```

Install/verify at least:

```text
gopls
dlv
```

Reload VS Code:

```text
Ctrl + Shift + P
Developer: Reload Window
```

## 14. VS Code launch.json

In this setup the VS Code workspace root is:

```text
/mnt/code/project/gpms/chartplotter
```

while the actual Go project is:

```text
/mnt/code/project/gpms/chartplotter/chartplotter
```

Create:

```text
/mnt/code/project/gpms/chartplotter/.vscode/launch.json
```

with:

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "Chartplotter - Debug Serve",
            "type": "go",
            "request": "launch",
            "mode": "exec",
            "program": "${workspaceFolder}/chartplotter/bin/chartplotter-debug",
            "cwd": "${workspaceFolder}/chartplotter",
            "args": [
                "serve"
            ],
            "console": "integratedTerminal"
        }
    ]
}
```

`program` must point to the debug binary and `cwd` should be the actual Go project root.

## 15. First useful breakpoint

Open:

```text
cmd/chartplotter/serve.go
```

The server command starts at:

```go
func (c serveCmd) Run() error {
```

A useful first breakpoint is near:

```go
catalogDir := c.S101
```

The HTTP server start can be found with:

```bash
grep -Rni "http.Server\|ListenAndServe\|Serve(" \
    cmd internal | head -50
```

In the current source, the important startup code is in:

```text
cmd/chartplotter/serve.go
```

and includes:

```go
return http.ListenAndServe(addr, srv)
```

This is also one of the first areas to review during production hardening.

## 16. Start debugging in VS Code

Open `cmd/chartplotter/serve.go`, then press:

```text
F9
```

to add a breakpoint.

Select:

```text
Chartplotter - Debug Serve
```

and press:

```text
F5
```

A successful session should show:

```text
PAUSED ON BREAKPOINT
```

and VS Code should populate:

```text
VARIABLES
WATCH
CALL STACK
BREAKPOINTS
```

A real call stack can look like:

```text
main.serveCmd.Run
main.(*serveCmd).Run
reflect.Value.Call
kong.callAnyFunction
kong.callFunction
kong.(*Context).RunNode
main.main
```

This confirms Delve is debugging the actual Chartplotter Go source.

## 17. Verified full debug chain

The successful chain is:

```text
VS Code
    ↓
Go extension
    ↓
Delve / DAP
    ↓
chartplotter-debug
    ↓
Go server
    ↓
CGO
    ↓
libtile57.a
    ↓
tile57 Zig engine
```

When the browser successfully loads `http://localhost:8080`, the development/debug environment is fully operational.

## 18. Common error: libtile57.a not found

Example:

```text
cannot find .../chartplotter/tile57/zig-out/lib/libtile57.a
No such file or directory
```

Cause: tile57 was built outside the Chartplotter repository, for example:

```text
/mnt/code/project/gpms/chartplotter/tile57
```

instead of:

```text
/mnt/code/project/gpms/chartplotter/chartplotter/tile57
```

Fix:

```bash
cd /mnt/code/project/gpms/chartplotter/chartplotter/tile57
zig build -Doptimize=Debug

cd ..

CGO_ENABLED=1 go build \
    -gcflags="all=-N -l" \
    -o bin/chartplotter-debug \
    ./cmd/chartplotter
```

Do not manually copy only `libtile57.a`; keep tile57 as the repository submodule.

## 19. Common error: cannot add a breakpoint

Install the official VS Code Go extension and ensure the editor glyph margin is enabled.

VS Code setting:

```json
{
    "editor.glyphMargin": true
}
```

You can also toggle a breakpoint with:

```text
F9
```

## 20. Common error: dlv not found

```bash
ls -l ~/go/bin/dlv
export PATH="$HOME/go/bin:$PATH"
which dlv
dlv version
```

## 21. Common error: debug binary is stale

For Go-only changes rebuild:

```bash
CGO_ENABLED=1 go build \
    -gcflags="all=-N -l" \
    -o bin/chartplotter-debug \
    ./cmd/chartplotter
```

If tile57 changed, rebuild it first:

```bash
cd tile57
zig build -Doptimize=Debug
zig build test -Doptimize=Debug
cd ..

CGO_ENABLED=1 go build \
    -gcflags="all=-N -l" \
    -o bin/chartplotter-debug \
    ./cmd/chartplotter
```

## 22. Recommended daily workflow

For Go-only work:

```bash
cd /mnt/code/project/gpms/chartplotter/chartplotter

CGO_ENABLED=1 go build \
    -gcflags="all=-N -l" \
    -o bin/chartplotter-debug \
    ./cmd/chartplotter
```

Then press `F5` in VS Code.

For tile57 changes:

```bash
cd /mnt/code/project/gpms/chartplotter/chartplotter/tile57

zig build -Doptimize=Debug
zig build test -Doptimize=Debug

cd ..

CGO_ENABLED=1 go build \
    -gcflags="all=-N -l" \
    -o bin/chartplotter-debug \
    ./cmd/chartplotter
```

Then press `F5` in VS Code.

## 23. Recommended future Makefile target

Add a dedicated debug target later:

```makefile
debug:
	cd tile57 && zig build -Doptimize=Debug
	CGO_ENABLED=1 go build \
		-gcflags="all=-N -l" \
		-o bin/chartplotter-debug \
		./cmd/chartplotter
```

Then developers can use:

```bash
make debug
```

while keeping:

```bash
make build
```

for the normal production/release build.

## 24. Production-hardening starting point

The first server file to review is:

```text
cmd/chartplotter/serve.go
```

The current startup path uses:

```go
http.ListenAndServe(addr, srv)
```

Production-hardening work should review:

- HTTP server timeouts
- graceful shutdown
- SIGTERM/SIGINT handling
- request size limits
- authentication and authorization
- rate limiting
- health and readiness endpoints
- structured logging and audit logging
- metrics and pprof policy
- connection/concurrency limits
- storage/cache lifecycle
- background jobs
- panic recovery
- reverse-proxy/TLS trust configuration

Responsibility split:

```text
Go server = HTTP/API/security/concurrency/operations

tile57    = ENC decoding/portrayal/tiles/PMTiles

Odoo/GPMS = users/business logic/routes/permissions/reporting
```

Do not modify tile57 for normal server-hardening work unless the problem is actually inside the chart engine.

## 25. Final verification checklist

```text
[ ] git submodule update --init --recursive succeeds
[ ] zig build -Doptimize=Debug succeeds
[ ] zig build test -Doptimize=Debug succeeds
[ ] echo $? returns 0
[ ] tile57/zig-out/lib/libtile57.a exists
[ ] file libtile57.a reports "current ar archive"
[ ] go.mod replace points to ./tile57/bindings/go
[ ] CGO debug build succeeds
[ ] bin/chartplotter-debug exists
[ ] binary contains debug_info and is not stripped
[ ] ./bin/chartplotter-debug --help works
[ ] dlv version works
[ ] dlv exec ./bin/chartplotter-debug -- serve works
[ ] VS Code Go extension is installed
[ ] launch.json points to the correct Chartplotter folder
[ ] F5 starts the server
[ ] breakpoint in serve.go is hit
[ ] browser can open http://localhost:8080
[ ] Chartplotter map UI renders successfully
```

## Quick command reference

```bash
cd /mnt/code/project/gpms/chartplotter/chartplotter

git submodule update --init --recursive

cd tile57
zig build -Doptimize=Debug
zig build test -Doptimize=Debug
echo $?
ls -lh zig-out/lib/libtile57.a
file zig-out/lib/libtile57.a

cd ..

CGO_ENABLED=1 go build \
    -gcflags="all=-N -l" \
    -o bin/chartplotter-debug \
    ./cmd/chartplotter

ls -lh bin/chartplotter-debug
file bin/chartplotter-debug
./bin/chartplotter-debug --help

./bin/chartplotter-debug serve

# or debug with Delve
dlv exec ./bin/chartplotter-debug -- serve
```

## Current verified result

The GPMS development environment used to prepare this guide successfully reached:

```text
tile57 Debug tests            PASS
tile57 libtile57.a            PASS
Go local tile57 binding       PASS
CGO link                      PASS
Go debug binary               PASS
Delve                         PASS
VS Code breakpoint            PASS
HTTP server startup           PASS
tile57 embedded catalogue     PASS
Chartplotter browser UI       PASS
```

Next phase: production hardening of the Go HTTP server.
