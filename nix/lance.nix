{ pkgs }:
let
  revision = "350060612087e1138ffa1bbb11a535013558241a";
  lockSHA256 = "e69a51c14dab6a9b7c412c763fdb6a5e1fe0089fd5199f944a482d4ced2b32ca";
  patchSHA256 = "f1c3261a7b59a1890af0ae0b457cf0ebf1614508cd039cec6d390df0218d7388";
  registry = builtins.readFile ../internal/extension/builtin.go;
  # The locked source-built Rust uses LLVM 21, which rejects Rust 1.98's
  # AVX512 intrinsic declarations. Use the same-version official distribution
  # and its matching bundled LLVM, already hash-pinned by this Nixpkgs input.
  rustPlatform = pkgs.makeRustPlatform {
    inherit (pkgs.rust.packages.prebuilt) rustc cargo;
  };
  source = pkgs.fetchFromGitHub {
    owner = "lance-format";
    repo = "lance-duckdb";
    rev = revision;
    hash = "sha256-aMZt44sNjyUPeD5QRzVbldHighwfK3AZ0XYcWtZSRoA=";
  };
  lockedCargoDeps = rustPlatform.importCargoLock { lockFile = ./lance-Cargo.lock; };
  patchedCargoDeps = pkgs.runCommand "leapview-lance-patched-cargo-vendor" {
    nativeBuildInputs = [ pkgs.python3 pkgs.patch ];
  } ''
    cp -R ${lockedCargoDeps} "$out"
    chmod u+w "$out"
    # Cargo's locked vendor directory contains immutable crate symlinks. Copy
    # only the two patched crates, retaining every other exact locked input.
    for version in 0.37.5 0.38.4; do
      crate="quick-xml-$version"
      rm "$out/$crate"
      cp -RL "${lockedCargoDeps}/$crate" "$out/$crate"
      chmod -R u+w "$out/$crate"
    done
    python3 ${./.}/apply-quick-xml-backports.py "$out" --patch-command ${pkgs.patch}/bin/patch
  '';
  rust =
    assert rustPlatform.rust.rustc.version == "1.98.1";
    assert rustPlatform.rust.cargo.version == "1.98.1";
    rustPlatform.buildRustPackage {
      pname = "leapview-lance-ffi";
      version = "${builtins.substring 0 12 revision}-smithy-json-0.62.7";
      src = source;
      cargoDeps = patchedCargoDeps;
      postPatch = ''
        cp ${./lance-Cargo.lock} Cargo.lock
      '';
      nativeBuildInputs = [ pkgs.protobuf pkgs.cmake pkgs.perl ];
      PROTOC = "${pkgs.protobuf}/bin/protoc";
      # Run upstream session/cache and dataset-write FFI tests; the consuming
      # DuckDB derivation also verifies the actual static extension registration.
      doCheck = true;
      cargoTestFlags = [ "--lib" ];
      postCheck = ''
        # Exercise both selected cloud-response parsers through the same
        # patched vendor source Cargo used for the FFI build, without network.
        smoke=$(mktemp -d "$TMPDIR/quick-xml-smoke.XXXXXX")
        mkdir "$smoke/src"
        cp ${./quick-xml-smoke.rs} "$smoke/src/lib.rs"
        cp Cargo.lock "$smoke/Cargo.lock"
        cat > "$smoke/Cargo.toml" <<'TOML'
        [package]
        name = "leapview-xml-backport-smoke"
        version = "0.1.0"
        edition = "2021"
        [dependencies]
        xml037 = { package = "quick-xml", version = "=0.37.5", features = ["serialize"] }
        xml038 = { package = "quick-xml", version = "=0.38.4", features = ["serialize"] }
        serde = { version = "=1.0.228", features = ["derive"] }
        [features]
        patched_configuration = []
        TOML
        CARGO_TARGET_DIR="$smoke/target" cargo test --offline --manifest-path "$smoke/Cargo.toml" \
          --features patched_configuration --target ${pkgs.stdenv.hostPlatform.rust.rustcTarget}
      '';
      installPhase = ''
        mkdir -p "$out/lib"
        cp target/${pkgs.stdenv.hostPlatform.rust.rustcTarget}/release/liblance_duckdb_ffi.a "$out/lib/"
      '';
      passthru = { inherit revision source; };
    };
in
assert builtins.hashFile "sha256" ./lance-Cargo.lock == lockSHA256;
assert builtins.hashFile "sha256" ./quick-xml-backport-lock.json == patchSHA256;
assert pkgs.lib.hasInfix ''SourceRevision:    "${revision}"'' registry;
assert pkgs.lib.hasInfix ''CargoLockSHA256:   "${lockSHA256}"'' registry;
assert pkgs.lib.hasInfix ''SourcePatchSHA256: "${patchSHA256}"'' registry;
{
  inherit rust revision;
  # Reuse the exact upstream C++ wrapper, linking the independently locked Rust
  # output. No build-time Cargo resolver/network or extension signing key.
  source = pkgs.runCommand "leapview-lance-static-source" { nativeBuildInputs = [ pkgs.python3 ]; } ''
    cp -R ${source} "$out"
    chmod -R u+w "$out"
    python3 - "$out/CMakeLists.txt" <<'PY'
    import pathlib, sys
    path = pathlib.Path(sys.argv[1])
    source = path.read_text()
    start = source.index("# Build and link Rust staticlib")
    end = source.index("target_link_libraries(", start)
    source = source[:start] + '''set(RUST_DEBUG_LIB "${rust}/lib/liblance_duckdb_ffi.a")
    set(RUST_RELEASE_LIB "${rust}/lib/liblance_duckdb_ffi.a")
    add_custom_target(lance_duckdb_ffi_build)
    ''' + source[end:]
    path.write_text(source)
    PY
  '';
}
