{
  description = "LeapView development toolchain";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # Keep Chromium's revision aligned with the npm Playwright lock, independently
    # of host/toolchain upgrades. Reuse upstream packaging rather than patch npm.
    playwright-nixpkgs = {
      url = "github:NixOS/nixpkgs/f2676046a1cba86d6f86a64f5b6d427b2f4dec96";
      flake = false;
    };
  };

  outputs =
    { nixpkgs, playwright-nixpkgs, ... }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      toolchain = import ./nix/toolchain.nix { inherit pkgs playwright-nixpkgs; };
    in
    {
      devShells.${system}.default = pkgs.mkShell {
        packages = toolchain.packages;
        buildInputs = [ pkgs.stdenv.cc.cc.lib ];
        inherit (toolchain) GOTOOLCHAIN PLAYWRIGHT_BROWSERS_PATH;
        PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
        LEAPVIEW_NIX_PLAYWRIGHT_VERSION = toolchain.playwrightVersion;
        BUN_FEATURE_FLAG_NO_ORPHANS = "1";
      };
      checks.${system}.toolchain = import ./nix/check-toolchain.nix {
        inherit pkgs toolchain;
      };
      formatter.${system} = pkgs.nixfmt;
    };
}
