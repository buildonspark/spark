# Browserify bitcoinjs-lib

As recommended in bitcoinjs-lib docs [here](https://github.com/bitcoinjs/bitcoinjs-lib?tab=readme-ov-file#browser) you can use browserify to make the library work in browser. Their instructions are not exactly correct, you should add the following to a temporary file entry.js:

```js
module.exports = require('bitcoinjs-lib');
```

and then run:

```sh
npm install -g bitcoinjs-lib browserify
npx browserify --standalone bitcoin -o bitcoinjs-lib.js entry.js
```
