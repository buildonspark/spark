declare module "*.wasm" {
  // When using esbuild's binary loader the default export is a Uint8Array
  const bytes: Uint8Array;
  export default bytes;
}
