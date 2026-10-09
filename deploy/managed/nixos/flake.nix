{
  description = "LeapView managed host scaffold (not a qualified production profile)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    disko = {
      url = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    deploy-rs = {
      url = "github:serokell/deploy-rs";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    nixos-anywhere = {
      url = "github:nix-community/nixos-anywhere";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.disko.follows = "disko";
    };
  };

  outputs =
    inputs@{
      self,
      nixpkgs,
      disko,
      deploy-rs,
      nixos-anywhere,
      ...
    }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      inventory = import ./inventory.example.nix;
      mkHost =
        role:
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            disko.nixosModules.disko
            ./modules/disk.nix
            self.nixosModules.${role}
            inventory.common
            inventory.${role}
          ];
        };
    in
    {
      nixosModules = {
        app = import ./modules/app.nix;
        database = import ./modules/database.nix;
      };
      nixosConfigurations = {
        example-app = mkHost "app";
        example-database = mkHost "database";
      };
      deploy.nodes = nixpkgs.lib.mapAttrs (name: host: {
        hostname = host.config.networking.hostName + ".invalid";
        sshUser = "root";
        profiles.system = {
          user = "root";
          path = deploy-rs.lib.${system}.activate.nixos host;
        };
        # This confirms activation/connectivity, not database/application health.
        magicRollback = true;
        autoRollback = true;
      }) self.nixosConfigurations;
      checks.${system} = deploy-rs.lib.${system}.deployChecks self.deploy // {
        host-contracts = import ./tests/contracts.nix {
          inherit pkgs;
          hosts = self.nixosConfigurations;
        };
      };
      packages.${system} = {
        port-probe = import ./tests/port-probe.nix { inherit pkgs; };
        kamal-transport-predecessor = import ./tests/kamal-transport-image.nix { inherit pkgs; };
        kamal-transport-candidate = import ./tests/kamal-transport-image.nix {
          inherit pkgs;
          revision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
        };
        kamal-transport-proxy = pkgs.dockerTools.pullImage {
          imageName = "basecamp/kamal-proxy";
          imageDigest = "sha256:826a6f66c6ba26ac26197ac8755804403c9bb617b90cfac25c7972154c5328ab";
          hash = "sha256-TDOr33IYV0+TmNBIgepu1S7dhYnYwvVkUnatp+r3QOQ=";
          finalImageName = "basecamp/kamal-proxy";
          finalImageTag = "v0.9.2";
          os = "linux";
          arch = "amd64";
        };
        kamal-transport-tools = pkgs.symlinkJoin {
          name = "leapview-kamal-transport-tools";
          paths = with pkgs; [
            distribution
            openssh
            openssl
            ruby
            bundler
            python3
            iproute2
            iptables
            util-linux
            curl
            dnsmasq
            slirp4netns
            coreutils
            bash
          ];
        };
        boot-test = import ./tests/boot.nix {
          inherit pkgs;
          modules = self.nixosModules;
          deployRs = deploy-rs;
        };
        fresh-install-bios = import ./tests/fresh-install.nix {
          inherit pkgs disko;
          host = self.nixosConfigurations.example-app;
          bootMode = "bios";
        };
        fresh-install-uefi = import ./tests/fresh-install.nix {
          inherit pkgs disko;
          host = self.nixosConfigurations.example-app;
          bootMode = "uefi";
        };
      };
      formatter.${system} = pkgs.nixfmt;
      devShells.${system}.default = pkgs.mkShell {
        packages = [
          pkgs.opentofu
          pkgs.ruby
          pkgs.bundler
          pkgs.nixfmt
          deploy-rs.packages.${system}.default
          nixos-anywhere.packages.${system}.default
        ];
      };
    };
}
