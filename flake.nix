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
      linuxSystems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
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
      toolchain = import ./nix/toolchain.nix { inherit pkgs playwright-nixpkgs; };
      packagesFor =
        targetSystem:
        let
          targetPkgs = nixpkgs.legacyPackages.${targetSystem};
          targetToolchain = import ./nix/toolchain.nix {
            pkgs = targetPkgs;
            inherit playwright-nixpkgs;
          };
          developmentBuild = import ./nix/application.nix {
            pkgs = targetPkgs;
            toolchain = targetToolchain;
            inherit revision dirty buildTime;
            purpose = "development";
            src = source;
          };
          composeBuild = import ./nix/application.nix {
            pkgs = targetPkgs;
            toolchain = targetToolchain;
            inherit revision dirty buildTime;
            purpose = "compose";
            src = source;
          };
          site = import ./nix/site.nix {
            pkgs = targetPkgs;
            toolchain = targetToolchain;
            src = source;
            inherit revision dirty;
          };
          siteImage = import ./nix/site-image.nix {
            pkgs = targetPkgs;
            inherit
              site
              revision
              dirty
              buildTime
              ;
          };
          developmentRuntime = import ./nix/patched-runtime.nix {
            pkgs = targetPkgs;
            application = developmentBuild;
          };
          composeRuntime = import ./nix/patched-runtime.nix {
            pkgs = targetPkgs;
            application = composeBuild;
          };
          application = developmentRuntime.application;
          composeApplication = composeRuntime.application;
          assets = import ./nix/runtime-assets.nix {
            pkgs = targetPkgs;
            toolchain = targetToolchain;
            application = developmentBuild;
            src = source;
          };
          portable = import ./nix/portable.nix {
            pkgs = targetPkgs;
            application = application;
            toolchain = targetToolchain;
          };
          composePortable = import ./nix/portable.nix {
            pkgs = targetPkgs;
            application = composeApplication;
            toolchain = targetToolchain;
          };
          image = import ./nix/image.nix {
            pkgs = targetPkgs;
            application = application;
            inherit
              assets
              revision
              dirty
              buildTime
              ;
            portable = portable;
            patchedRuntime = developmentRuntime;
            purpose = "development";
          };
          composeImage = import ./nix/image.nix {
            pkgs = targetPkgs;
            application = composeApplication;
            inherit
              assets
              revision
              dirty
              buildTime
              ;
            portable = composePortable;
            patchedRuntime = composeRuntime;
            purpose = "compose";
          };
          controllers =
            if targetSystem == system then
              let
                developmentCLI = import ./nix/deployment-cli.nix {
                  pkgs = targetPkgs;
                  toolchain = targetToolchain;
                  inherit revision dirty buildTime;
                  src = source;
                  purpose = "development";
                };
                composeCLI = import ./nix/deployment-cli.nix {
                  pkgs = targetPkgs;
                  toolchain = targetToolchain;
                  inherit revision dirty buildTime;
                  src = source;
                  purpose = "compose";
                };
                desktop = import ./nix/desktop.nix {
                  pkgs = targetPkgs;
                  toolchain = targetToolchain;
                  src = source;
                };
              in
              {
                leapviewctl-linux-amd64 = developmentCLI;
                leapviewctl-linux-arm64 = developmentCLI.arm64;
                leapviewctl-compose-linux-amd64 = composeCLI;
                leapviewctl-compose-linux-arm64 = composeCLI.arm64;
                leapview-desktop-linux-x64 = desktop;
              }
            else
              { };
        in
        {
          default = application;
          leapview = application;
          leapview-image = image;
          leapview-linux = portable;
          leapview-compose = composeApplication;
          leapview-image-compose = composeImage;
          leapview-linux-compose = composePortable;
          leapview-site = site.package;
          leapview-site-image = siteImage;
          leapview-tools = developmentBuild.tools;
          map-assets = assets.maps;
          extension-supply = assets.extensions;
          glibc-runtime = developmentRuntime.glibc;
          go-dependencies = developmentBuild.dependencies.go;
          javascript-dependencies = developmentBuild.dependencies.javascript;
        }
        // controllers;
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
            nativePkgs.stdenv.cc
            nativePkgs.patchelf
            nativePkgs.jq
            nativePkgs.curl
          ];
          inherit (nativeToolchain) GOTOOLCHAIN;
        };
    in
    {
      packages = pkgs.lib.genAttrs linuxSystems packagesFor;
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
