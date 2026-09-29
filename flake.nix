{
  description = "LeapView development toolchain and application builds";

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
    {
      self,
      nixpkgs,
      playwright-nixpkgs,
      ...
    }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      revision = pkgs.lib.removeSuffix "-dirty" (self.rev or self.dirtyRev or "unknown");
      dirty = !(self ? rev);
      date = self.lastModifiedDate or "19700101000000";
      part = offset: length: builtins.substring offset length date;
      buildTime = "${part 0 4}-${part 4 2}-${part 6 2}T${part 8 2}:${part 10 2}:${part 12 2}Z";
      # Image recipes, CI and local tooling do not change application source.
      source = pkgs.lib.cleanSourceWith {
        src = self;
        filter =
          path: type:
          let
            relative = pkgs.lib.removePrefix "${self}/" (toString path);
          in
          !(builtins.elem relative [
            "nix"
            ".github"
            "flake.nix"
            "flake.lock"
            "Taskfile.yml"
            "AGENTS.md"
            "scripts/check_nix_development.mjs"
            "scripts/check_nix_image.sh"
          ]);
      };
      assets = import ./nix/runtime-assets.nix {
        inherit pkgs toolchain application;
        src = source;
      };
      portable = import ./nix/portable.nix { inherit pkgs application toolchain; };
      image = import ./nix/image.nix {
        inherit
          pkgs
          application
          assets
          portable
          revision
          dirty
          buildTime
          ;
      };
      application = import ./nix/application.nix {
        inherit
          pkgs
          toolchain
          revision
          dirty
          buildTime
          ;
        src = source;
      };
      toolchain = import ./nix/toolchain.nix { inherit pkgs playwright-nixpkgs; };
    in
    {
      packages.${system} = {
        default = application;
        leapview = application;
        leapview-image = image;
        leapview-linux = portable;
        map-assets = assets.maps;
        extension-supply = assets.extensions;
        go-dependencies = application.dependencies.go;
        javascript-dependencies = application.dependencies.javascript;
      };
      devShells.${system}.default = pkgs.mkShell {
        packages = toolchain.packages;
        buildInputs = [ pkgs.stdenv.cc.cc.lib ];
        inherit (toolchain) GOTOOLCHAIN PLAYWRIGHT_BROWSERS_PATH FONTCONFIG_FILE;
        PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
        LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION = toolchain.playwrightVersion;
        BUN_FEATURE_FLAG_NO_ORPHANS = "1";
      };
      checks.${system}.toolchain = import ./nix/check-toolchain.nix {
        inherit pkgs toolchain;
      };
      formatter.${system} = pkgs.nixfmt;
    };
}
