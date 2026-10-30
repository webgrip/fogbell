---
paths:
  - "**/*.tf"
  - "**/*.tftest.hcl"
  - "**/.terraform.lock.hcl"
---

# OpenTofu

- Use OpenTofu (`tofu`) at the version the repository pins, never `terraform`, because the state encryption and the CI images are OpenTofu's.
- Before committing, run `tofu fmt -recursive`, then `tofu init -backend=false` and `tofu validate`, and `tofu test` where the root has tests, because CI runs the same checks without credentials on every push.
- Commit `.terraform.lock.hcl` whenever `tofu init` changes it, because CI installs exactly the providers the lock file names.
- Keep the `encryption` block `enforced = true` for both state and plan, and never rename its key provider or method, because the encrypted state embeds those names and a rename makes it unreadable.
- Leave `tofu apply` to the repository's applier (the CI job on `main`, or tofu-controller in the cluster), except for the break-glass or import path the repository documents, because an apply from a laptop or a branch skips the reviewed plan.
- Give a resource whose loss loses data or a node `prevent_destroy`, because a plan that replaces it should fail rather than apply.
- Stop managing, rename or adopt a resource with `removed {}`, `moved {}` and `import {}` blocks, because a block is reviewed in the plan and a `tofu state` command is not.
- Mark a variable or output that carries a credential `sensitive = true` and pass its value from OpenBao through `TF_VAR_*`, because a `*.tfvars` file in Git or an unmarked output prints the value in every plan.
