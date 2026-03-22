{self}: {
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.programs."3mf2stl";

  convertScript = pkgs.writeShellScriptBin "3mf2stl-convert" ''
    #!/bin/bash
    input_file="$1"
    if [[ "$input_file" == *.3mf ]]; then
      output_file="''${input_file%.3mf}.stl"
      ${lib.getExe cfg.package} "$input_file" "$output_file"
      ${pkgs.xdg-utils}/bin/xdg-open "$output_file"
    fi
  '';
in {
  options.programs."3mf2stl" = {
    enable = mkEnableOption "3mf2stl Converter and 3D design environment";

    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      readOnly = true;
      description = "The 3mf2stl package to use.";
    };
  };

  config = mkIf cfg.enable {
    home.packages = [
      cfg.package
    ];

    xdg.desktopEntries."3mf2stl" = {
      name = "3mf2stl Converter";
      exec = "${convertScript}/bin/3mf2stl-convert %f";
      mimeType = ["model/3mf"];
      categories = ["Graphics"];
      comment = "Convert 3MF to STL and open";
    };

    xdg.mimeApps = let
      pairs = {"model/3mf" = "3mf2stl.desktop";};
    in {
      associations.added = pairs;
      defaultApplications = pairs;
    };
  };
}
