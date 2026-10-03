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
            "scripts/testdata/nix"
          ]);
      };
      assets = import ./nix/runtime-assets.nix {
        inherit pkgs toolchain application;
        src = source;
      };
      patchedRuntime = import ./nix/patched-runtime.nix {
        inherit pkgs;
        application = applicationBuild;
      };
      application = patchedRuntime.application;
      deploymentCLI = import ./nix/deployment-cli.nix {
        inherit
          pkgs
          toolchain
          revision
          dirty
          buildTime
          ;
        src = source;
      };
      portable = import ./nix/portable.nix { inherit pkgs application toolchain; };
      image = import ./nix/image.nix {
        inherit
          pkgs
          application
          assets
          portable
          patchedRuntime
          revision
          dirty
          buildTime
          ;
      };
      applicationBuild = import ./nix/application.nix {
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
      runtimeSecurityShell =
        platform:
        let
          nativePkgs = nixpkgs.legacyPackages.${platform};
          nativeToolchain = import ./nix/toolchain.nix {
            pkgs = nativePkgs;
            inherit playwright-nixpkgs;
          };
        in
        nativePkgs.mkShell {
          packages = [
            nativeToolchain.go
            nativePkgs.syft
            nativePkgs.grype
            nativePkgs.skopeo
            nativePkgs.gh
            nativePkgs.python3
            nativePkgs.docker-client
          ];
          inherit (nativeToolchain) GOTOOLCHAIN;
        };
    in
    {
      packages.${system} = {
        default = application;
        leapview = application;
        leapview-image = image;
        leapview-linux = portable;
        leapviewctl-linux-amd64 = deploymentCLI;
        leapviewctl-linux-arm64 = deploymentCLI.arm64;
        map-assets = assets.maps;
        extension-supply = assets.extensions;
        glibc-runtime = patchedRuntime.glibc;
        go-dependencies = applicationBuild.dependencies.go;
        javascript-dependencies = applicationBuild.dependencies.javascript;
      };
      devShells.${system} = {
        orchestration = pkgs.mkShellNoCC {
          packages = toolchain.orchestrationPackages;
          inherit (toolchain) GOTOOLCHAIN;
        };
        default = pkgs.mkShell {
          packages = toolchain.packages;
          buildInputs = [ pkgs.stdenv.cc.cc.lib ];
          # Bun's native Parcel watcher loads the locked C++ runtime at execution.
          LD_LIBRARY_PATH = pkgs.lib.makeLibraryPath [ pkgs.stdenv.cc.cc.lib ];
          inherit (toolchain) GOTOOLCHAIN PLAYWRIGHT_BROWSERS_PATH FONTCONFIG_FILE;
          PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
          LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION = toolchain.playwrightVersion;
          BUN_FEATURE_FLAG_NO_ORPHANS = "1";
        };
        runtime-security = runtimeSecurityShell system;
      };
      devShells.aarch64-linux.runtime-security = runtimeSecurityShell "aarch64-linux";
      checks.${system}.toolchain = import ./nix/check-toolchain.nix {
        inherit pkgs toolchain;
      };
      formatter.${system} = pkgs.nixfmt;
    };
}
