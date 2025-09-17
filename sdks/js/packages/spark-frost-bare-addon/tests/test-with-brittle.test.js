/*

There's currently an issue with installing brittle, the bare testing lib, with yarn.
We're waiting for a fix from the bare team, see LIG-8107. Once supported the tests
will be something like:

const test = require('brittle')
const addon = require('.')

test('hello', (t) => {
  t.is(addon.hello(), 'Hello from Rust')
})

*/

Bare.exit(0);
