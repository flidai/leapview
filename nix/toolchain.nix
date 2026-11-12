{ pkgs, playwright-nixpkgs }:
let
  manifest = builtins.fromJSON (builtins.readFile ../package.json);
  goVersion = pkgs.lib.removePrefix "go " (
    pkgs.lib.findFirst (line: pkgs.lib.hasPrefix "go " line) (throw "go.mod lacks a Go version") (
      pkgs.lib.splitString "\n" (builtins.readFile ../go.mod)
    )
  );
  bunVersion = pkgs.lib.removePrefix "bun@" manifest.packageManager;
  # Keep the locked Nix packaging and host-data patches while taking the official
  # security releases for GO-2026-6859. Hashes are from go.dev/dl/?mode=json.
  patchedGo =
    package: version: hash:
    package.overrideAttrs {
      inherit version;
      src = pkgs.fetchurl {
        url = "https://go.dev/dl/go${version}.src.tar.gz";
        inherit hash;
      };
    };
  go =
    assert goVersion == "1.27.4";
    patchedGo pkgs.go_1_27 goVersion "sha256-c8+4VxrsDAEXdofbhxY9Rqog6XeWcncokaoXZU2aYJk=";
  bunArtifact =
    if pkgs.stdenv.hostPlatform.isx86_64 then
      {
        name = "bun-linux-x64-baseline.zip";
        sha256 = "a063908ae08b7852ca10939bbdc6ceed3ddabce8fb9402dce83d65d73b36e6c7";
      }
    else if pkgs.stdenv.hostPlatform.isAarch64 then
      {
        name = "bun-linux-aarch64.zip";
        # GitHub's release API digest for the official bun-v1.3.14 asset.
        sha256 = "a27ffb63a8310375836e0d6f668ae17fa8d8d18b88c37c821c65331973a19a3b";
      }
    else
      throw "LeapView's Nix application toolchain supports x86_64-linux and aarch64-linux";
  bun = pkgs.bun.overrideAttrs {
    version = bunVersion;
    src = pkgs.fetchurl {
      url = "https://github.com/oven-sh/bun/releases/download/bun-v${bunVersion}/${bunArtifact.name}";
      inherit (bunArtifact) sha256;
    };
  };
  # sqlc currently deliberately selects this version through GOTOOLCHAIN. Expose
  # a native Nix executable so Go never downloads an unpatched Linux toolchain.
  sqlcCompiler =
    patchedGo pkgs.go_1_26 "1.26.10"
      "sha256-/WPog7ovK/ypPBZGsx6+9j4rHk0rMXyXidl2po2B828=";
  sqlcGo = pkgs.writeShellScriptBin "go1.26.10" ''
    unset GOROOT
    exec ${sqlcCompiler}/bin/go "$@"
  '';
  playwright =
    (pkgs.callPackage "${playwright-nixpkgs}/pkgs/development/web/playwright/driver.nix" { })
    .playwright-core;
  fontConfigRules = pkgs.runCommand "leapview-fontconfig-rules" { } ''
    mkdir -p "$out"
    for config in ${pkgs.fontconfig.out}/etc/fonts/conf.d/*.conf; do
      case "$(basename "$config")" in
        50-user.conf|51-local.conf) continue ;;
      esac
      ln -s "$config" "$out/$(basename "$config")"
    done
  '';
  fontconfig =
    (pkgs.makeFontsConf {
      # Keep browser text metrics independent of runner/user font installations.
      impureFontDirectories = [ ];
      includes = [ fontConfigRules ];
      fontDirectories = [
        pkgs.dejavu_fonts
        pkgs.liberation_ttf
        # Playwright's conventional Linux dependencies include this fallback.
        # ZRender measures 国 to derive chart line heights even for Latin labels.
        pkgs.wqy_zenhei
      ];
    }).overrideAttrs
      (previous: {
        # makeFontsConf unconditionally adds the user's XDG font directory.
        buildCommand = previous.buildCommand + ''
          sed -i '\|<dir prefix="xdg">fonts</dir>|d' "$out"
        '';
      });
  browsers = playwright.selectBrowsers {
    withFirefox = false;
    withWebkit = false;
    fontconfig_file = fontconfig;
  };
in
assert manifest.devDependencies."@playwright/test" == "^${playwright.version}";
{
  inherit
    go
    goVersion
    bun
    bunVersion
    sqlcGo
    ;
  playwrightVersion = playwright.version;
  GOTOOLCHAIN = "local";
  # Planning/gating compile pure Go and verify GitHub performance evidence.
  # This closure is the bounded orchestration archive: git plumbing needs no
  # Perl or manuals, and the gate runs node without npm or C headers.
  orchestrationPackages = with pkgs; [
    go
    gitMinimal
    jq
    python3
    bash
    coreutils
    nodejs-slim_24.out
    gh
    unzip
  ];
  # The upstream headless-shell package does not inherit the Chromium wrapper.
  FONTCONFIG_FILE = fontconfig;
  PLAYWRIGHT_BROWSERS_PATH = "${browsers}";
  packages = with pkgs; [
    go
    bun
    sqlcGo
    nodejs_24
    go-task
    pkg-config
    procps
    stdenv.cc
    patchelf
    git
    curl
    jq
    openssl
    python3
    gnumake
    postgresql_18
    docker-client
    docker-compose
    nixfmt
    actionlint
    gh
    unzip
  ];
}
