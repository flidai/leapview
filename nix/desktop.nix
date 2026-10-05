{
  pkgs,
  toolchain,
  src,
}:
let
  dependencies = import ./dependencies.nix { inherit pkgs toolchain src; };
  desktopManifest = builtins.fromJSON (builtins.readFile (src + "/desktop/package.json"));
  releasePolicy = builtins.fromJSON (builtins.readFile (src + "/desktop/release-policy.json"));
  linuxSupport = pkgs.lib.findFirst (
    entry: entry.platform == "linux"
  ) null releasePolicy.supportMatrix;
  electronVersion = releasePolicy.runtime.electron;
  electronCacheDirectory = builtins.hashString "sha256" "https://github.com/electron/electron/releases/download/v${electronVersion}";
  electronZipFile = "electron-v${electronVersion}-linux-x64.zip";
  nodeVersion = releasePolicy.runtime.node;
  electronArchive = pkgs.fetchurl {
    url = "https://github.com/electron/electron/releases/download/v${electronVersion}/electron-v${electronVersion}-linux-x64.zip";
    # SHA-256 checked against the official v44.4.3 SHASUMS256.txt release file.
    hash = "sha256-/ogKfjcWDP1OABk7xMcT6teneKv+dIYKLTbYb9C+SKg=";
  };
  # Nix rejects real setuid chmod. Keep MakerDeb's chmod and archive writer in
  # one fake metadata session; its inner fakeroot invocation cannot nest.
  nestedFakerootDispatcher = pkgs.writeShellScriptBin "fakeroot" ''
    if [ -n "''${FAKEROOTKEY:-}" ]; then
      exec "$@"
    fi
    exec ${pkgs.fakeroot}/bin/fakeroot "$@"
  '';
  nodeArchive = pkgs.fetchurl {
    url = "https://nodejs.org/dist/v${nodeVersion}/node-v${nodeVersion}-linux-x64.tar.xz";
    # SHA-256 checked against the official Node v26.9.0 SHASUMS256.txt file.
    hash = "sha256-xuzY78HB05UmWJFnUxnaens3hca+F0vXiTPPTwV2JNE=";
  };
  nodeRuntime = pkgs.stdenvNoCC.mkDerivation {
    pname = "leapview-desktop-node-runtime";
    version = nodeVersion;
    src = nodeArchive;
    nativeBuildInputs = [
      pkgs.autoPatchelfHook
      pkgs.gnutar
      pkgs.xz
    ];
    buildInputs = [ pkgs.stdenv.cc.cc.lib ];
    dontUnpack = true;
    installPhase = ''
      mkdir -p "$TMPDIR/node" "$out/bin"
      tar -xJf "$src" -C "$TMPDIR/node" --strip-components=1 node-v${nodeVersion}-linux-x64/bin/node
      install -m 0755 "$TMPDIR/node/bin/node" "$out/bin/node"
    '';
    # autoPatchelfHook adapts this build-only helper to the Nix build sandbox.
    # It is not copied into the packaged Electron application.
  };
in
assert pkgs.stdenv.hostPlatform.system == "x86_64-linux";
assert linuxSupport != null && linuxSupport.architectures == [ "x64" ];
assert linuxSupport.minimumVersion == "Ubuntu 22.04 LTS";
assert releasePolicy.distribution.linux.installer == "deb";
assert releasePolicy.runtime.electron == desktopManifest.devDependencies.electron;
assert releasePolicy.runtime.node == desktopManifest.devDependencies.node;
assert releasePolicy.runtime.bun == toolchain.bunVersion;
pkgs.stdenvNoCC.mkDerivation {
  pname = "leapview-desktop-linux-x64";
  version = desktopManifest.version;
  inherit src;

  nativeBuildInputs = [
    toolchain.bun
    nodeRuntime
    pkgs.autoPatchelfHook
    pkgs.dpkg
    pkgs.fakeroot
    pkgs.gnutar
    pkgs.nodejs_24
    pkgs.unzip
    pkgs.xz
  ];
  buildInputs = [ pkgs.stdenv.cc.cc.lib ];
  dontConfigure = true;
  dontAutoPatchelf = true;
  dontFixup = true;
  BUN_FEATURE_FLAG_NO_ORPHANS = "1";
  DPKG_DEB_THREADS_MAX = "2";
  PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";

  buildPhase = ''
    runHook preBuild
    export npm_config_cache="$TMPDIR/npm-cache"
    export BUN_INSTALL_CACHE_DIR="$TMPDIR/bun-cache"
    export PATH="${nodeRuntime}/bin:$PATH"
    mkdir -p "$npm_config_cache" "$BUN_INSTALL_CACHE_DIR"

    cp -R ${dependencies.desktopJavascript}/node_modules desktop/node_modules
    chmod -R u+w desktop/node_modules
    ${dependencies.prunePlatformPackages} "$PWD/desktop/node_modules" "x64"
    # This package contains Linux glibc and musl binaries in one package.json;
    # the generic dependency pruner cannot distinguish those files.
    rm desktop/node_modules/@electron-internal/extract-zip/index.linux-x64-musl.node
    patchShebangs desktop/node_modules
    autoPatchelf desktop/node_modules

    mkdir -p desktop/node_modules/node/bin
    install -m 0755 ${nodeRuntime}/bin/node desktop/node_modules/node/bin/node
    mkdir -p desktop/node_modules/electron/dist
    unzip -q ${electronArchive} -d desktop/node_modules/electron/dist
    printf '%s\n' '${electronVersion}' > desktop/node_modules/electron/dist/version
    printf '%s' 'electron' > desktop/node_modules/electron/path.txt
    export XDG_CACHE_HOME="$TMPDIR/xdg-cache"
    mkdir -p "$XDG_CACHE_HOME/electron/${electronCacheDirectory}"
    ln -s ${electronArchive} "$XDG_CACHE_HOME/electron/${electronCacheDirectory}/${electronZipFile}"

    (
      cd desktop
      bun run build
      export PATH="${nestedFakerootDispatcher}/bin:$PATH"
      LEAPVIEW_DESKTOP_DISTRIBUTION=preview ${pkgs.fakeroot}/bin/fakeroot bun scripts/run-electron.mjs make
    )
    runHook postBuild
  '';

  installPhase = ''
    set -o pipefail
    installers=()
    while IFS= read -r -d $'\0' installer; do
      installers+=("$installer")
    done < <(find desktop/out/make -type f -name '*.deb' -print0)
    if [ "''${#installers[@]}" -ne 1 ]; then
      echo "expected exactly one Linux x64 Debian package, found ''${#installers[@]}" >&2
      exit 1
    fi
    dpkg-deb --fsys-tarfile "''${installers[0]}" \
      | tar -tvf - \
      | awk '$1 == "-rwsr-xr-x" && $2 == "root/root" && $6 == "./usr/lib/leapview-desktop/chrome-sandbox" { found++ } END { if (found != 1) { print "Debian chrome-sandbox must be root-owned mode 04755" > "/dev/stderr"; exit 1 } }'
    mkdir -p "$out"
    install -m 0644 "''${installers[0]}" "$out/leapview-desktop-linux-x64.deb"
    test -s "$out/leapview-desktop-linux-x64.deb"
  '';

  meta = {
    description = "LeapView Linux x64 desktop Debian package candidate";
    platforms = [ "x86_64-linux" ];
  };
}
