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
        boot-test = import ./tests/boot.nix {
          inherit pkgs;
          modules = self.nixosModules;
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
