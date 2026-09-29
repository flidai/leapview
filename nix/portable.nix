{
  pkgs,
  application,
  toolchain,
}:
pkgs.runCommand "leapview-linux-${application.version}"
  {
    nativeBuildInputs = [
      toolchain.go
      pkgs.patchelf
      pkgs.binutils
    ];
  }
  ''
    mkdir -p "$out/bin"
    cp ${application}/bin/{leapview,leapviewctl} "$out/bin/"
    chmod u+w "$out/bin/"*
    for binary in "$out/bin/"*; do
      # Exported tools use the host's supported glibc/libstdc++ runtime. Native
      # Nix users retain the store-linked application output instead.
      patchelf --no-sort --set-interpreter /lib64/ld-linux-x86-64.so.2 --remove-rpath "$binary"
      go version -m "$binary" > "$TMPDIR/build-info"
      grep -Fq 'github.com/flidai/leapview' "$TMPDIR/build-info"
      grep -Fq 'github.com/jackc/pgx/v5' "$TMPDIR/build-info"
      for abi in GLIBC:2.38 GLIBCXX:3.4.30 CXXABI:1.3.13; do
        family="''${abi%%:*}"
        baseline="''${abi#*:}"
        required=$(readelf --version-info "$binary" | grep -oE "$family"'_[0-9.]+' | sed "s/$family\_//" | sort -Vu | tail -1)
        newest=$(printf '%s\n' "$baseline" "$required" | sort -V | tail -1)
        if [ "$newest" != "$baseline" ]; then
          echo "$binary requires $family $required, above supported $baseline" >&2
          exit 1
        fi
      done
      if patchelf --print-needed "$binary" | grep -q '/'; then
        echo "exported binary has an absolute library dependency: $binary" >&2
        exit 1
      fi
    done
  ''
