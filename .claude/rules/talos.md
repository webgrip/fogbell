---
paths:
  - "**/talos/**"
---

# Talos

- Keep the Talos and Kubernetes versions in `talos/talenv.yaml` under their `# renovate:` lines, because the config generator, the node image build and Renovate all read them there.
- Change `configContract` only as a planned migration, separate from a `talosVersion` bump, because it fixes the shape of the generated machine config documents and existing patches can collide with the new shape.
- Add or remove a system extension in the Image Factory schematic, never in a machine config patch, because extensions ship in the installer image the schematic names.
- Apply machine config through the repository's path (its `just talos-*` recipe or the OpenTofu apply), never a hand `talosctl apply-config`, because a hand apply drifts from Git and can reboot a control plane.
