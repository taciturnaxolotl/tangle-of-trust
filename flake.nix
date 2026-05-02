{
  description = "the tangled web of trust — visualize the tangled.social trust graph";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs =
    {
      self,
      nixpkgs,
    }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
        "x86_64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (system: let
        pkgs = nixpkgs.legacyPackages.${system};

        webDist = pkgs.buildNpmPackage {
          pname = "tangle-of-trust-web";
          version = "0.1.0";
          src = ./.;
          npmDepsHash = "sha256-wGdPHKwLoedBX/JljRl/8qMn8LaL8X/lYmOP3PnMCyM=";
          npmBuildCommand = "npx vite build";
          installPhase = ''
            mkdir -p $out
            cp -r web-dist $out/
          '';
        };
      in {
        default = pkgs.buildGoModule {
          pname = "tangle-of-trust";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-qU8hOL2z/5Fjjczf374Enumxc0Shfxlit35AJNKIEV4=";

          preBuild = ''
            mkdir -p web-dist
            cp -r ${webDist}/web-dist/* web-dist/
          '';

          passthru.web-dist = "${webDist}/web-dist";

          meta = with pkgs.lib; {
            description = "Visualize the tangled.social trust graph as an interactive force-directed graph";
            homepage = "https://tangle.dunkirk.sh";
            license = licenses.mit;
            platforms = platforms.unix;
          };
        };

        web-dist = webDist;
      });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/ingest";
        };
      });

      devShells = forAllSystems (system: {
        default =
          let
            pkgs = nixpkgs.legacyPackages.${system};
          in
          pkgs.mkShell {
            packages = with pkgs; [
              go
              nodejs
              sqlite
            ];

            shellHook = ''
              export DATABASE_URL="tangle.db"
            '';
          };
      });
    };
}
