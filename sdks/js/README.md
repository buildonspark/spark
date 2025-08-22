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

Then install dependencies for all workspaces:

```
# cd to js or to any subdirectory of js
cd spark/sdks/js
yarn
```

Please note there is a postinstall script that runs after install to build some dependencies. This will run automatically when the dependency tree changes or when manually running `yarn rebuild`.

tmp
