{
  pkgs,
  toolchain,
  src,
}:
let
  hashes = builtins.fromJSON (builtins.readFile ./build-hashes.json);
  nativeCPU =
    if pkgs.stdenv.hostPlatform.isx86_64 then
      "x64"
    else if pkgs.stdenv.hostPlatform.isAarch64 then
      "arm64"
    else
      throw "LeapView's Nix application supports x86_64-linux and aarch64-linux";
  prunePlatformPackages = pkgs.writeShellScript "prune-npm-platform-packages" ''
    node - "$1" "$2" <<'NODE'
    const fs = require('node:fs');
    const path = require('node:path');
    const [root, cpuList] = process.argv.slice(2);
    const cpus = cpuList.split(',');
    const matches = (rule, value) => {
      const values = Array.isArray(rule) ? rule : [rule];
      if (values.includes('*') || values.includes('any')) return true;
      const allowed = values.filter((entry) => !entry.startsWith('!'));
      return values.every((entry) => !entry.startsWith('!') || entry.slice(1) !== value) &&
        (allowed.length === 0 || allowed.includes(value));
    };
    const accepts = (rule, values) => values.some((value) => matches(rule, value));
    const prune = (directory) => {
      for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
        const candidate = path.join(directory, entry.name);
        let stat;
        try { stat = fs.statSync(candidate); } catch { continue; }
        if (!stat.isDirectory()) continue;
        const manifest = path.join(candidate, 'package.json');
        if (fs.existsSync(manifest)) {
          const metadata = JSON.parse(fs.readFileSync(manifest, 'utf8'));
          if ((metadata.cpu && !accepts(metadata.cpu, cpus)) ||
              (metadata.os && !matches(metadata.os, 'linux')) ||
              (metadata.libc && !matches(metadata.libc, 'glibc'))) {
            fs.rmSync(candidate, { recursive: true, force: true });
          } else {
            const nested = path.join(candidate, 'node_modules');
            if (fs.existsSync(nested)) prune(nested);
          }
        } else {
          prune(candidate);
        }
      }
    };
    prune(root);
    NODE
  '';
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
  inherit nativeCPU prunePlatformPackages;
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
    bun install --frozen-lockfile --ignore-scripts --cpu='*' --os=linux
    ${prunePlatformPackages} "$PWD/node_modules" "x64,arm64"
    mkdir -p "$out/app" "$out/typespec"
    cp -R node_modules "$out/app/"
    for cpu in x64 arm64; do
      typespec="$TMPDIR/typespec-$cpu"
      mkdir -p "$typespec"
      cp pkg/apigen/typespec/package.json pkg/apigen/typespec/package-lock.json "$typespec/"
      (
        cd "$typespec"
        npm ci --ignore-scripts --no-audit --no-fund --os=linux --cpu="$cpu" --libc=glibc
      )
      mkdir -p "$out/typespec/$cpu"
      cp -R "$typespec/node_modules" "$out/typespec/$cpu/"
    done
  '';
}
