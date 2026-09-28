{
  pkgs,
  toolchain,
  src,
}:
let
  hashes = builtins.fromJSON (builtins.readFile ./build-hashes.json);
  fixed =
    name: hash: script:
    pkgs.stdenvNoCC.mkDerivation {
      inherit name src;
      nativeBuildInputs = [
        toolchain.go
        toolchain.sqlcGo
        toolchain.bun
        pkgs.nodejs_24
        pkgs.cacert
      ];
      dontConfigure = true;
      dontFixup = true;
      outputHashMode = "recursive";
      outputHashAlgo = "sha256";
      outputHash = hash;
      GOTOOLCHAIN = "local";
      SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
      buildPhase = ''
        export GOPATH="$TMPDIR/go" GOCACHE="$TMPDIR/go-cache"
        export npm_config_cache="$TMPDIR/npm-cache"
        export BUN_INSTALL_CACHE_DIR="$TMPDIR/bun-cache"
        export PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
        ${script}
      '';
      installPhase = "true";
    };
in
{
  go = fixed "leapview-go-dependencies" hashes.go ''
    go mod download all
    (cd pkg/apigen; go mod download all)
    mkdir "$TMPDIR/sqlc"
    (cd "$TMPDIR/sqlc"
      go mod init leapview-build-sqlc
      go mod edit -go=1.26.7 -require=github.com/sqlc-dev/sqlc@v1.31.1
      GOTOOLCHAIN=go1.26.7 go mod download all)
    mkdir -p "$out"
    cp -R "$GOPATH/pkg/mod/cache/download" "$out/download"
    # Only module data belongs in the immutable proxy, not sumdb/cache state.
    rm -rf "$out/download/sumdb"
    find "$out" -type f ! -name '*.zip' ! -name '*.mod' ! -name '*.info' -delete
    # go run module@version checks deprecation through the proxy's version list.
    while IFS= read -r directory; do
      find "$directory" -maxdepth 1 -name '*.mod' -printf '%f\n' | sed 's/\.mod$//' | sort > "$directory/list"
    done < <(find "$out/download" -type d -name @v)
  '';
  javascript = fixed "leapview-javascript-dependencies" hashes.javascript ''
    bun install --frozen-lockfile --ignore-scripts
    npm --prefix pkg/apigen/typespec ci --ignore-scripts --no-audit --no-fund
    mkdir -p "$out/app" "$out/typespec"
    cp -R node_modules "$out/app/"
    cp -R pkg/apigen/typespec/node_modules "$out/typespec/"
  '';
}
