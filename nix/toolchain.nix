{ pkgs, playwright-nixpkgs }:
let
  manifest = builtins.fromJSON (builtins.readFile ../package.json);
  goVersion = pkgs.lib.removePrefix "go " (
    pkgs.lib.findFirst (line: pkgs.lib.hasPrefix "go " line) (throw "go.mod lacks a Go version") (
      pkgs.lib.splitString "\n" (builtins.readFile ../go.mod)
    )
  );
  bunVersion = pkgs.lib.removePrefix "bun@" manifest.packageManager;
  # Fixed-output overrides retain the application's existing versions while
  # using upstream Nixpkgs build/patching logic. Update hashes with manifests.
  go = pkgs.go_1_26.overrideAttrs {
    version = goVersion;
    src = pkgs.fetchurl {
      url = "https://go.dev/dl/go${goVersion}.src.tar.gz";
      hash = "sha256-Tjm5jkL5RvoFrIvFtxh335fb23y7Gnd7VBZnrXEX/S4=";
    };
  };
  bun = pkgs.bun.overrideAttrs {
    version = bunVersion;
    src = pkgs.fetchurl {
      url = "https://github.com/oven-sh/bun/releases/download/bun-v${bunVersion}/bun-linux-x64-baseline.zip";
      sha256 = "a063908ae08b7852ca10939bbdc6ceed3ddabce8fb9402dce83d65d73b36e6c7";
    };
  };
  # sqlc currently deliberately selects this version through GOTOOLCHAIN. Expose
  # a native Nix executable so Go never downloads an unpatched Linux toolchain.
  sqlcGo =
    assert pkgs.go_1_26.version == "1.26.7";
    pkgs.writeShellScriptBin "go1.26.7" ''
      unset GOROOT
      exec ${pkgs.go_1_26}/bin/go "$@"
    '';
  playwright =
    (pkgs.callPackage "${playwright-nixpkgs}/pkgs/development/web/playwright/driver.nix" { })
    .playwright-core;
  fontconfig = pkgs.makeFontsConf {
    fontDirectories = [
      pkgs.dejavu_fonts
      pkgs.liberation_ttf
    ];
  };
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
  ];
}
