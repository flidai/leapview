{ pkgs, toolchain }:
pkgs.runCommand "leapview-development-toolchain-check"
  {
    nativeBuildInputs = toolchain.packages;
    buildInputs = [ pkgs.stdenv.cc.cc.lib ];
    inherit (toolchain) GOTOOLCHAIN;
  }
  ''
    export GOCACHE="$TMPDIR/go-cache"
    test "$(go env GOVERSION)" = "go${toolchain.goVersion}"
    test "$(bun --version)" = "${toolchain.bunVersion}"
    GOTOOLCHAIN=go1.26.9 go version | grep -F 'go1.26.9'
    node --version
    task --version
    docker --version
    docker compose version
    docker-compose --version
    # Exercise the native compiler and runtime, not just package version strings.
    cat > probe.go <<'GO'
    package main
    /* int answer(void) { return 42; } */
    import "C"
    func main() { if C.answer() != 42 { panic("CGO probe failed") } }
    GO
    CGO_ENABLED=1 go build -o probe probe.go
    ./probe
    mkdir -p "$out"
    printf '%s\n' 'toolchain and CGO runtime passed' > "$out/result"
  ''
