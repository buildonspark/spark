# Embedded SDK dependencies

`sdk-licenses.js` generates a dependency inventory and third-party notices after
each binding build. Reports accompany the artifacts in the binding manifest and
in the published npm packages. For example:

```sh
node public/scripts/sdk-licenses.js generate frost-wasm
node public/scripts/sdk-licenses.js generate frost-bare --bare-target linux-x64
```

The generator reads the existing artifacts without rebuilding them. Run it with
the Rust toolchain and Cargo lockfiles corresponding to the committed bindings.
This public snapshot names its token binding groups `token-wasm`, `token-android`,
and `token-ios`; these resolve to the `spark-token-primitives-uniffi` crate.
After generating all affected reports, refresh the binding manifest with
`node public/scripts/sdk-bindings.js update` and verify it with `check`.

The inventory is conservative: it follows normal and build dependencies,
excludes development-only edges, and includes all target-specific dependencies.
Some listed crates may not be linked into the final binary. It records artifact hashes
so that recipients can identify the accompanying build. Nested license files cover
native code vendored inside Rust crates, such as libsecp256k1. The Rust distribution's
standard-library copyright report covers runtime components that don't appear
in the application's Cargo lockfile.

Crate notices are generated as `NOTICES.txt`. Rust's upstream report is copied
byte-for-byte to `RUST-NOTICES.html` and referenced from the text file; it is not
parsed or converted. The inventory records the Rust report's filename and hash.

For MPL crates, the generator verifies the published source file checksums before
linking to the exact source archive. If the source is modified, distribute the
modified MPL source and update the generator's source-availability handling before
publishing binaries. Also review generated Kotlin and Swift helper code copied
from UniFFI templates when modifying those files.

When a crate omits license files, only CC0 and MPL may use the standard texts in
`licenses/`. An attribution template cannot replace an upstream copyright notice;
Cargo's author list is not evidence of copyright ownership.

Other missing-license cases require a package/version-specific entry in
`upstream/overrides.json`. Each reviewed entry records the declared and selected
license, the published crate's checksum, its `.cargo_vcs_info.json` revision, and
the source URLs and checksums of the checked-in upstream files. The generator
verifies these before using an override and includes the provenance in the
reports. Preserve applicable `NOTICE`, `COPYRIGHT`, and license-bearing `AUTHORS`
files as well as the selected license text. Builds do not fetch these files.

The `hex_lit` override records a reviewed exception: its checksum-pinned source
archive contains the MITNFA declaration but no copyright statements or license
file, and its recorded Git revision is unavailable upstream. The override retains
the published README and the standard MITNFA text without inventing attribution.
