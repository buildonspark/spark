# @buildonspark/create-spark-app

## 0.0.1

### Patch Changes

- Fix file exclusion filter, template instructions, and help text
  - Fix exclusion filter incorrectly matching files like `build.mjs` (broke browser-extension template)
  - Fix express and nestjs templates using wrong script name (`dev` -> `start:dev`)
  - Fix nodejs-scripts template referencing non-existent file path
  - Add iOS/Android section headers to react-native template instructions
  - Clean up target directory on download or transform failure
  - Error when no files are extracted from tarball (e.g. wrong branch or template)
  - Verify redirect response status code before extracting
  - Handle branch names with slashes in tarball prefix
  - Show full scoped package name in help text and README
  - Add `npm create @buildonspark/spark-app` shorthand to README
