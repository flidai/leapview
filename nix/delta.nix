{ pkgs }:
let
  policy = builtins.fromJSON (builtins.readFile ./delta-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "delta";
  };
  command = "${pkgs.python3}/bin/python3 ${receipts.source}/scripts/nix_native_delta_receipt.py";
  rustPlatform = pkgs.makeRustPlatform { inherit (pkgs.rust.packages.prebuilt) rustc cargo; };
  kernelSource = pkgs.fetchFromGitHub {
    owner = "delta-io";
    repo = "delta-kernel-rs";
    rev = policy.kernel.revision;
    hash = policy.kernel.sourceNARHash;
  };
  lockedCargoDeps = rustPlatform.importCargoLock { lockFile = ./delta-Cargo.lock; };
  patchedCargoDeps =
    pkgs.runCommand "leapview-delta-patched-cargo-vendor"
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
        rm "$out/quick-xml-0.39.2"
        cp -RL ${lockedCargoDeps}/quick-xml-0.39.2 "$out/quick-xml-0.39.2"
        chmod -R u+w "$out/quick-xml-0.39.2"
        python3 ${receipts.source}/nix/apply-quick-xml-backports.py "$out" --patch-command ${pkgs.patch}/bin/patch \
          --policy delta-quick-xml-backport-lock.json
      '';
  source = pkgs.applyPatches {
    name = "leapview-delta-source";
    src = pkgs.fetchFromGitHub {
      owner = "duckdb";
      repo = "duckdb-delta";
      rev = policy.wrapper.revision;
      hash = policy.wrapper.sourceNARHash;
    };
    patches = [ ./delta-static-dependencies.patch ];
    postPatch = ''
      ${pkgs.python3}/bin/python3 ${./check-extension-version.py} src/delta_extension.cpp src/include/delta_extension.hpp \
        ${pkgs.stdenv.cc}/bin/c++ DeltaExtension EXT_VERSION_DELTA ${policy.wrapper.revision}
      ${pkgs.python3}/bin/python3 - ${./delta-source-lock.json} <<'PY'
      import hashlib, json, pathlib, sys
      expected = json.loads(pathlib.Path(sys.argv[1]).read_text())['wrapper']['headerTransformFiles']
      assert {name: hashlib.sha256(pathlib.Path(name).read_bytes()).hexdigest() for name in expected} == expected
      PY
    '';
  };
  rust = rustPlatform.buildRustPackage {
    pname = "leapview-delta-kernel-ffi";
    version = policy.kernel.version;
    src = kernelSource;
    cargoDeps = patchedCargoDeps;
    cargoBuildFeatures = policy.features;
    cargoBuildFlags = [
      "--package=delta_kernel_ffi"
      "--message-format=json-render-diagnostics"
    ];
    cargoTestFlags = [
      "--package=delta_kernel_ffi"
      "--lib"
    ];
    nativeBuildInputs = [
      pkgs.cmake
      pkgs.perl
    ];
    postPatch = ''
      mkdir -p "$TMPDIR/native-evidence"
      ${command} patches --repo ${receipts.source} --output-root "$cargoDepsCopy" --destination "$TMPDIR/native-evidence/patches.json"
      ${command} source --repo ${receipts.source} --output-root "$PWD" --destination "$TMPDIR/native-evidence/source.json"
    '';
    buildPhase = ''
      rustc --version --verbose > "$TMPDIR/native-evidence/compiler.txt"
      cargo --version > "$TMPDIR/native-evidence/cargo.txt"
      set -o pipefail
      cargoBuildHook | ${receipts.command} capture-cargo --repo ${receipts.source} --platform ${receipts.platform} \
        --destination "$TMPDIR/native-evidence/cargo.jsonl"
    '';
    postBuild = ''
      cp target/ffi-headers/delta_kernel_ffi.h target/ffi-headers/delta_kernel_ffi.hpp "$TMPDIR/native-evidence/"
      bash ${source}/scripts/ffi/generate_delta_kernel_ffi_header ${source}/scripts/ffi \
        "$TMPDIR/native-evidence/delta_kernel_ffi.hpp" "$TMPDIR/native-evidence"
      ${command} headers --repo ${receipts.source} --output-root "$TMPDIR/native-evidence" \
        --destination "$TMPDIR/native-evidence/headers.json"
    '';
    doCheck = true;
    postCheck = ''
      smoke=$(mktemp -d "$TMPDIR/delta-xml-smoke.XXXXXX")
      mkdir "$smoke/src"
      cp ${./delta-quick-xml-smoke.rs} "$smoke/src/lib.rs"
      cp Cargo.lock "$smoke/Cargo.lock"
      cat > "$smoke/Cargo.toml" <<'TOML'
      [package]
      name = "leapview-delta-xml-backport-smoke"
      version = "0.1.0"
      edition = "2021"
      [dependencies]
      xml039 = { package = "quick-xml", version = "=0.39.2", features = ["serialize"] }
      serde = { version = "=1.0.228", features = ["derive"] }
      [features]
      patched_configuration = []
      TOML
      CARGO_TARGET_DIR="$smoke/target" cargo test --offline --manifest-path "$smoke/Cargo.toml" \
        --features patched_configuration --target ${pkgs.stdenv.hostPlatform.rust.rustcTarget}
      echo 'Delta selected FFI upstream unit tests passed' > "$TMPDIR/native-evidence/checks.txt"
    '';
    installPhase = ''
      mkdir -p "$out/lib" "$out/include"
      cp target/${pkgs.stdenv.hostPlatform.rust.rustcTarget}/release/libdelta_kernel_ffi.a "$out/lib/"
      for name in delta_kernel_ffi.h delta_kernel_ffi.hpp generated_delta_kernel_ffi.hpp generated_inline_msvc_compat.inc; do
        cp "$TMPDIR/native-evidence/$name" "$out/include/$name"
      done
    '';
    postFixup = ''
      mkdir -p "$out/share/leapview"
      ${receipts.command} component --repo ${receipts.source} --platform ${receipts.platform} --component delta \
        --evidence "$TMPDIR/native-evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
    '';
    passthru = { inherit kernelSource source; };
  };
in
assert rustPlatform.rust.rustc.version == policy.compilerVersion;
assert rustPlatform.rust.cargo.version == policy.compilerVersion;
assert builtins.hashFile "sha256" ./delta-Cargo.lock == policy.kernel.cargoLockSHA256;
assert builtins.hashFile "sha256" ./delta-static-dependencies.patch == policy.wrapper.patchSHA256;
assert
  builtins.hashFile "sha256" ./delta-quick-xml-backport-lock.json == (builtins.head policy.patches)
  .sha256;
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./delta-source-lock.json) (
  builtins.readFile ../internal/extension/builtin.go
);
{
  inherit source rust;
  revision = policy.wrapper.revision;
}
