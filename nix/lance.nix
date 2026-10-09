{ pkgs }:
let
  revision = "350060612087e1138ffa1bbb11a535013558241a";
  lockSHA256 = "9d7e406bb9174769960775d7f75233b01e4a96a9bd9be8de3040be1badc91839";
  registry = builtins.readFile ../internal/extension/builtin.go;
  source = pkgs.fetchFromGitHub {
    owner = "lance-format";
    repo = "lance-duckdb";
    rev = revision;
    hash = "sha256-aMZt44sNjyUPeD5QRzVbldHighwfK3AZ0XYcWtZSRoA=";
  };
  rust = pkgs.rustPlatform.buildRustPackage {
    pname = "leapview-lance-ffi";
    version = "${builtins.substring 0 12 revision}-smithy-json-0.62.7";
    src = source;
    cargoLock.lockFile = ./lance-Cargo.lock;
    postPatch = ''
      cp ${./lance-Cargo.lock} Cargo.lock
    '';
    nativeBuildInputs = [ pkgs.protobuf pkgs.cmake pkgs.perl ];
    PROTOC = "${pkgs.protobuf}/bin/protoc";
    # Run upstream session/cache and dataset-write FFI tests; the consuming
    # DuckDB derivation also verifies the actual static extension registration.
    doCheck = true;
    cargoTestFlags = [ "--lib" ];
    installPhase = ''
      mkdir -p "$out/lib"
      cp target/${pkgs.stdenv.hostPlatform.rust.rustcTarget}/release/liblance_duckdb_ffi.a "$out/lib/"
    '';
    passthru = { inherit revision source; };
  };
in
assert builtins.hashFile "sha256" ./lance-Cargo.lock == lockSHA256;
assert pkgs.lib.hasInfix ''SourceRevision:  "${revision}"'' registry;
assert pkgs.lib.hasInfix ''CargoLockSHA256: "${lockSHA256}"'' registry;
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
