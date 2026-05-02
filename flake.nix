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
      packages = forAllSystems (system: {
        default =
          let
            pkgs = nixpkgs.legacyPackages.${system};
          in
          pkgs.buildGoModule {
            pname = "tangle-of-trust";
            version = "0.1.0";

            src = ./.;

            vendorHash = "sha256-qU8hOL2z/5Fjjczf374Enumxc0Shfxlit35AJNKIEV4=";

            meta = with pkgs.lib; {
              description = "Visualize the tangled.social trust graph as an interactive force-directed graph";
              homepage = "https://tangle.dunkirk.sh";
              license = licenses.mit;
              platforms = platforms.unix;
            };
          };
      });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/tangle-ingest";
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
