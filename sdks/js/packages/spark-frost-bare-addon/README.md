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
import { SparkWallet, BareSparkSigner } from "@buildonspark/bare" with {
  imports: "./imports.json",
};

let { wallet, mnemonic } = await SparkWallet.initialize({
  signer: new BareSparkSigner(),
});
const balance = await wallet.getBalance();
```

## Publishing

The committed package includes prebuilds for every tier 1 platform listed in the [Bare documentation](https://github.com/holepunchto/bare?tab=readme-ov-file#platform-support). The `SDK bindings` workflow rebuilds and commits all of them when the addon or one of its declared dependencies changes. Its manifest and package-content checks must pass before publishing; `prepublishOnly` also rejects stale or missing prebuilds. Publish from the committed package after the SDK bindings gate is green rather than downloading a separate workflow artifact.

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
