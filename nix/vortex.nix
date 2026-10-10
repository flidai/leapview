{ pkgs }:
let
  policy = builtins.fromJSON (builtins.readFile ./vortex-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "vortex";
  };
  command = "${pkgs.python3}/bin/python3 ${receipts.source}/scripts/nix_native_vortex_receipt.py";
  rustPlatform = pkgs.makeRustPlatform { inherit (pkgs.rust.packages.prebuilt) rustc cargo; };
  engineSource = pkgs.fetchFromGitHub {
    owner = "duckdb";
    repo = "duckdb";
    rev = policy.engine.revision;
    hash = policy.engine.sourceNARHash;
  };
  rustSource = pkgs.fetchFromGitHub {
    owner = "vortex-data";
    repo = "vortex";
    rev = policy.rust.revision;
    hash = policy.rust.sourceNARHash;
  };
  lockedCargoDeps = rustPlatform.importCargoLock {
    lockFile = ./vortex-Cargo.lock;
    outputHashes = policy.gitCargoSources;
  };
  patchedCargoDeps =
    pkgs.runCommand "leapview-vortex-patched-cargo-vendor"
      {
        nativeBuildInputs = [
          pkgs.python3
          pkgs.patch
        ];
      }
      ''
        cp -R ${lockedCargoDeps} "$out"
        chmod u+w "$out" "$out/.cargo" "$out/.cargo/config.toml"
        substituteInPlace "$out/.cargo/config.toml" --replace-fail 'directory = "cargo-vendor-dir"' 'directory = "@vendor@"'
        rm "$out/quick-xml-0.39.4"
        cp -RL ${lockedCargoDeps}/quick-xml-0.39.4 "$out/quick-xml-0.39.4"
        chmod -R u+w "$out/quick-xml-0.39.4"
        python3 ${receipts.source}/nix/apply-quick-xml-backports.py "$out" --patch-command ${pkgs.patch}/bin/patch \
          --policy vortex-quick-xml-backport-lock.json
      '';
  source = pkgs.applyPatches {
    name = "leapview-vortex-source";
    src = pkgs.fetchFromGitHub {
      owner = "vortex-data";
      repo = "duckdb-vortex";
      rev = policy.wrapper.revision;
      hash = policy.wrapper.sourceNARHash;
    };
    patches = [ ./vortex-static-dependencies.patch ];
    postPatch = ''
      ${pkgs.python3}/bin/python3 ${./check-extension-version.py} src/vortex_extension.cpp src/include/vortex_extension.hpp \
        ${pkgs.stdenv.cc}/bin/c++ VortexExtension EXT_VERSION_VORTEX ${policy.wrapper.revision}
    '';
  };
  rust = rustPlatform.buildRustPackage {
    pname = "leapview-vortex-duckdb-ffi";
    version = builtins.substring 0 12 policy.rust.revision;
    src = rustSource;
    patches = [ ./vortex-selected-engine.patch ];
    cargoDeps = patchedCargoDeps;
    nativeBuildInputs = [
      pkgs.cmake
      pkgs.perl
      pkgs.pkg-config
      rustPlatform.bindgenHook
      pkgs.llvmPackages.clang
      pkgs.llvmPackages.clang-tools
    ];
    # The upstream explicit source branch generates both directions of the ABI
    # and compiles its C++ bridge, returning before any downloaded engine path.
    DUCKDB_SOURCE_DIR = engineSource;
    DUCKDB_VERSION = policy.engine.version;
    CC_ENABLE_DEBUG_OUTPUT = "1";
    postPatch = ''
      mkdir -p "$TMPDIR/native-evidence"
      ${command} patches --repo ${receipts.source} --output-root "$cargoDepsCopy" --destination "$TMPDIR/native-evidence/patches.json"
      ${command} source --repo ${receipts.source} --output-root "$PWD" --destination "$TMPDIR/native-evidence/source.json"
      ${command} engine-source --repo ${receipts.source} --output-root ${engineSource} --destination "$TMPDIR/native-evidence/engine-source.json"
      printf '%s\n' ${engineSource} > "$TMPDIR/native-evidence/engine-path.txt"
    '';
    buildPhase = ''
      runHook preBuild
      rustc --version --verbose > "$TMPDIR/native-evidence/compiler.txt"
      cargo --version > "$TMPDIR/native-evidence/cargo.txt"
      "$CXX" --version > "$TMPDIR/native-evidence/cxx.txt"
      printf 'compiler-target: %s\n' "$($CXX -dumpmachine)" >> "$TMPDIR/native-evidence/cxx.txt"
      clang --version > "$TMPDIR/native-evidence/clang.txt"
      export CARGO_PROFILE_RELEASE_STRIP=false
      set -o pipefail
      ${pkgs.rust.envVars.setEnv} cargo rustc --offline --locked -j "$NIX_BUILD_CORES" \
        --target ${pkgs.stdenv.hostPlatform.rust.rustcTarget} --release --package vortex-duckdb --lib --crate-type=staticlib \
        --message-format=json-render-diagnostics | ${receipts.command} capture-cargo --repo ${receipts.source} --platform ${receipts.platform} \
        --destination "$TMPDIR/native-evidence/cargo.jsonl"
      runHook postBuild
    '';
    postBuild = ''
      cp vortex-duckdb/src/cpp.rs "$TMPDIR/native-evidence/cpp.rs"
      ${pkgs.python3}/bin/python3 - "$TMPDIR/native-evidence/cpp-build.txt" <<'PY'
      import pathlib, sys
      outputs = list(pathlib.Path('target/${pkgs.stdenv.hostPlatform.rust.rustcTarget}/release/build').glob('vortex-duckdb-*/output'))
      assert len(outputs) == 1, outputs
      data = outputs[0].read_bytes()
      assert len(data) <= 64 * 1024 * 1024
      pathlib.Path(sys.argv[1]).write_bytes(data)
      PY
    '';
    doCheck = true;
    # FFI tests need the final DuckDB binary. Pure file tests run here without an
    # engine dependency cycle; the native application lane exercises real FFI.
    cargoTestFlags = [
      "--package=vortex-file"
      "--lib"
    ];
    checkFeatures = [
      "object_store"
      "tokio"
    ];
    postCheck = ''
      smoke=$(mktemp -d "$TMPDIR/vortex-xml-smoke.XXXXXX")
      mkdir "$smoke/src"
      cp ${./delta-quick-xml-smoke.rs} "$smoke/src/lib.rs"
      cp Cargo.lock "$smoke/Cargo.lock"
      cat > "$smoke/Cargo.toml" <<'TOML'
      [package]
      name = "leapview-vortex-xml-backport-smoke"
      version = "0.1.0"
      edition = "2021"
      [dependencies]
      xml039 = { package = "quick-xml", version = "=0.39.4", features = ["serialize"] }
      serde = { version = "=1.0.228", features = ["derive"] }
      [features]
      patched_configuration = []
      TOML
      CARGO_TARGET_DIR="$smoke/target" cargo test --offline --manifest-path "$smoke/Cargo.toml" \
        --features patched_configuration --target ${pkgs.stdenv.hostPlatform.rust.rustcTarget}
      echo 'Vortex file upstream unit tests and selected XML boundary passed' > "$TMPDIR/native-evidence/checks.txt"
    '';
    installPhase = ''
      mkdir -p "$out/lib" "$out/include"
      cp target/${pkgs.stdenv.hostPlatform.rust.rustcTarget}/release/libvortex_duckdb.a "$out/lib/"
      cp vortex-duckdb/include/vortex.h "$out/include/"
      cp -R vortex-duckdb/cpp/include/. "$out/include/"
      ${command} headers --repo ${receipts.source} --output-root "$out" --destination "$TMPDIR/native-evidence/headers.json"
    '';
    postFixup = ''
      mkdir -p "$out/share/leapview"
      ${receipts.command} component --repo ${receipts.source} --platform ${receipts.platform} --component vortex \
        --evidence "$TMPDIR/native-evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
    '';
    passthru = { inherit rustSource engineSource source; };
  };
in
assert rustPlatform.rust.rustc.version == policy.compilerVersion;
assert rustPlatform.rust.cargo.version == policy.compilerVersion;
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./vortex-source-lock.json) (
  builtins.readFile ../internal/extension/builtin.go
);
assert builtins.hashFile "sha256" ./vortex-Cargo.lock == policy.rust.cargoLockSHA256;
assert builtins.hashFile "sha256" ./vortex-static-dependencies.patch == policy.wrapper.patchSHA256;
assert builtins.hashFile "sha256" ./vortex-selected-engine.patch == policy.rust.patchSHA256;
assert
  builtins.hashFile "sha256" ./vortex-quick-xml-backport-lock.json == (builtins.head policy.patches)
  .sha256;
{
  inherit source rust;
  revision = policy.wrapper.revision;
}
