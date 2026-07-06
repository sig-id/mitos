# The mitos harness rootfs for the Nucleus provider (G6 — the image-build path).
#
# Nucleus's `lib.mkAgentToolchainRootfs` bundles a full dev toolchain (bash, git,
# nodejs, python3, rust, cargo, go, gcc, make …) plus `providerPackages` for the
# injected harness. We build an OCI bundle (config.json + rootfs) that
# `nucleus create --bundle <path> -- <harness argv>` runs, so the harness executes
# under gVisor isolation instead of Nucleus's minimal agent rootfs.
#
# The model key is NOT baked here — it lives at the mitos edge (Vault + TlsMitm
# proxy); the harness makes uncredentialed calls the proxy authenticates.
#
# Build:  nix build .#oci-bundle  → result/
# Run:    cp -r result/ /tmp/bundle && chmod -R u+w /tmp/bundle
#         nucleus create --bundle /tmp/bundle -- <harness> <args>
{
  description = "mitos harness rootfs (OCI bundle) for the Nucleus provider";
  inputs.nucleus.url = "path:/media/data/proj/nucleus";
  inputs.nixpkgs.follows = "nucleus/nixpkgs";

  outputs = { self, nucleus, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };

      # The harness provider packages to install in the rootfs. nodejs is already
      # in the toolchain; add a harness here. pi/claude/codex/opencode are node
      # CLIs — install via npm into the rootfs (example: `pi`) or a nix derivation.
      # providerPackages are passed through to mkAgentToolchainRootfs verbatim.
      providerPackages = [ ];

      rootfs = nucleus.lib.mkAgentToolchainRootfs {
        inherit pkgs providerPackages;
        name = "mitos-harness-rootfs";
      };
    in {
      packages.${system} = {
        inherit rootfs;

        # An OCI bundle Nucleus accepts via `--bundle`. The process.args is a
        # placeholder; `nucleus create --bundle … -- <argv>` overrides it.
        oci-bundle = pkgs.runCommand "mitos-harness-oci-bundle" { } ''
          mkdir -p $out
          cp -a -P --no-preserve=mode,ownership ${rootfs}/. $out/rootfs/
          cat > $out/config.json <<'JSON'
          {
            "ociVersion": "1.0.1",
            "process": {
              "terminal": false,
              "user": { "uid": 0, "gid": 0 },
              "args": ["/bin/sh"],
              "cwd": "/workspace",
              "env": ["PATH=/bin:/usr/bin:/usr/local/bin"]
            },
            "root": { "path": "rootfs", "readonly": false },
            "hostname": "mitos",
            "mounts": []
          }
          JSON
        '';
      };
    };
}
