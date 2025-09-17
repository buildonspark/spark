# Bare addon for Spark SDK

This package provides Spark frost signer bindings for use in [Bare runtime](https://bare.pears.com/). This adds support for more platforms since the WASM bindings (used for Node.js and browsers) in spark-sdk are not supported in bare in e.g. < iOS 18.4.

## Build

```sh
yarn build

# Test the addon
yarn bare index.js
```

## Installing bare and using the addon

If running from the Spark JS workspaces running bare with `yarn bare` and `yarn bare-make` is recommended to use the common version installed. Alternatively you can install globally with npm:

```sh
npm i -g bare bare-make
```

From the Spark JS workspaces you can test running spark-sdk in bare from our [spark-bare-app](https://github.com/buildonspark/spark/tree/main/sdks/js/examples/spark-bare-app) example scripts or install it in your project and import from the @buildonspark/bare package:

```js
import {
  SparkWallet,
  BareSparkSigner,
} from "@buildonspark/bare" with { imports: "./imports.json" };

let { wallet, mnemonic } = await SparkWallet.initialize({
  signer: new BareSparkSigner(),
});
const balance = await wallet.getBalance();
```

## Publishing

When publishing the spark-frost-bare-addon to NPM you should include all prebuilds for tier 1 platforms listed in the [bare docs](https://github.com/holepunchto/bare?tab=readme-ov-file#platform-support). These docs link to an [example prebuild workflow](https://github.com/holepunchto/bare/blob/main/.github/workflows/prebuild.yml) on which our prebuild workflow is based, but ours also includes steps required to set up Rust to build the bindings. This workflow will run automatically on any changes to spark-frost-bare-addon files or its dependencies. Before publishing to NPM locally you should navigate to the "Bare prebuild" workflow run when the commit lands in main, click "Summary", and download the "prebuilds" articact which contains files for all platforms. Replace your local "prebuilds" directory with this one to ensure they're all included when you publish to NPM.

## Advanced build options

On MacOS be sure to prioritize the system toolchain instead of homebrew, otherwise you'll encounter errors for bare-make commands:

```sh
export PATH="/usr/bin:$PATH"
```

As mentioned in the [bare addon guide](https://github.com/holepunchto/bare-snippets/tree/main/addon-support) run the following:

```sh
yarn

cd spark-frost-bare-addon

# By default bare-make will target and build for your current platform
yarn bare-make generate && yarn bare-make build && yarn bare-make install

# Test the addon
yarn bare index.js

# To build for spark-bare-expo-react-native-app
yarn bare-make generate --platform ios --arch arm64 --simulator && yarn bare-make build && yarn bare-make install
# This seems to be necessary to build/install an additional target, otherwise it reuses the previous target:
rm -rf build

yarn bare-make generate --platform ios --arch arm64 && yarn bare-make build && yarn bare-make install
rm -rf build

yarn bare-make generate --platform ios --arch x64 && yarn bare-make build && yarn bare-make install
rm -rf build
```
