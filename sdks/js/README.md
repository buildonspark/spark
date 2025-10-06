# Spark JS SDK workspaces

## Install

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

Then install & build dependencies for all workspaces:

```
# cd to js or to any subdirectory of js
cd spark/sdks/js
yarn
yarn build
```

Please note there is a postinstall script that runs after install to build some dependencies. This will run automatically when the dependency tree changes or when manually running `yarn rebuild`.

## Running Examples

Example (`spark-node-express`):

```
cd spark/sdks/js
yarn
yarn build
cd examples/spark-node-express
yarn start
```

Refer to the individual README files in each directory for any additional instructions.

**Alternative: Use mise tasks from the repo root for common workflows:**

See root README for instructions on installing mise. Then see `mise.toml` for all tasks. Common ones:

```bash
# From the spark repo root
mise spark-cli-regtest      # Run CLI with regtest network
mise spark-cli-mainnet      # Run CLI with mainnet
mise test-js                # Run JS tests
```
