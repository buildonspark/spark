# Spark JS SDK workspaces

## Prerequisites

### System Dependencies

The postinstall build process requires a C/C++ compiler. Install clang:

**macOS:**

```bash
xcode-select --install
```

**Linux (Ubuntu/Debian):**

```bash
sudo apt install clang lld
```

### Node.js

You should use [nvm](https://github.com/nvm-sh/nvm#installing-and-updating) to manage your node versions. That way you're using the same version as CI, it's easier to upgrade, and easier to repro any issues that are tied to a specific version.

With nvm installed:

```
cd spark/sdks/js
nvm use || nvm install
```

Similarly to manage yarn versions [it's recommended](https://yarnpkg.com/getting-started/install) using corepack which is built in with node:

```
corepack enable
cd spark/sdks/js
# use yarn version from packageManager key in package.json:
corepack prepare --activate
```

Then install dependencies for all workspaces:

```
# cd to js or to any subdirectory of js
cd spark/sdks/js
yarn
```

Please note there is a postinstall script that runs after install to build some dependencies. This will run automatically when the dependency tree changes or when manually running `yarn rebuild`.

## Committed binding manifest

[`sdk-bindings-manifest.json`](sdk-bindings-manifest.json) is generated evidence for the committed SDK binding artifacts. Its hand-authored specification is [`public/scripts/sdk-bindings.json`](../../public/scripts/sdk-bindings.json).

For each binding group, the manifest records:

- `input_sha256`: one digest over the declared source files, their paths, and configured tool versions.
- `input_file_count`: protection against a glob silently adding or dropping source files.
- `artifacts`: the exact path and SHA-256 digest of every committed generated output.

Trigger-only inputs select a rebuild but are intentionally excluded from `input_sha256`. For example, the internal JavaScript lockfile triggers Bare regeneration without publishing a digest of that private input.

The manifest is generated after fresh artifacts are assembled and is committed with those artifacts. Do not edit it manually. From the repository root, regenerate its records only after producing the corresponding outputs:

```bash
node public/scripts/sdk-bindings.js update
node public/scripts/sdk-bindings.js check
```

`check` rejects changed source inputs, changed or missing artifacts, and undeclared files in managed output directories. `verify-packages` additionally performs npm dry runs and confirms every recorded artifact is included in its owning package. The manifest does not by itself prove how an artifact was built or that it passed runtime tests; CI establishes those properties by replacing committed outputs with artifacts from the current workflow run and exercising them on supported runtime targets.
