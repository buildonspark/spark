# SDK binding automation

[`sdk-bindings.json`](sdk-bindings.json) is the hand-authored source of truth for committed JavaScript SDK bindings. It defines:

- The FROST and token-primitives binding groups for WASM, Android, iOS, and Bare.
- The source inputs and trigger-only inputs that select each group for regeneration.
- The exact generated outputs owned by each group and npm package.
- The tool versions, managed output paths, and generated manifest location.
- The workflow policy used to select full rebuilds and protect privileged commits.

`trusted_producer_paths` protects the write-enabled follow-up workflow from allowing a pull request to redefine its own guardrails. The initial pull request workflow builds without a repository write token. Before a later workflow may commit generated artifacts with the bot token, it loads `sdk-bindings.json` and the commit validator from the default branch, then compares every listed path between the pull request head and that trusted revision. The complete matched path set, each Git blob SHA, and each executable mode must be identical.

Normal binding inputs such as Rust and proto sources may differ because those are the changes the bindings are meant to capture. A change to a producer-control file, such as a workflow, build wrapper, patcher, staging script, or validator, stops the privileged commit. That control change must land and become trusted before a later run can use it. The path list is not secret; the security property comes from loading the policy and validator from the trusted default branch.

The workflow generates and commits [`sdks/js/sdk-bindings-manifest.json`](../../sdks/js/sdk-bindings-manifest.json) alongside the declared outputs. Useful repository-root checks are:

```bash
node public/scripts/sdk-bindings.js check
node public/scripts/sdk-bindings.js verify-packages
```
