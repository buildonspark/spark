---
name: proto-regenerate
description: Regenerate proto bindings for both SO (Go) and SDK (TypeScript) after proto file changes
disable-model-invocation: true
---

# Proto Regeneration Workflow

When `.proto` files are modified, regenerate bindings for both codebases.

## Steps

**All commands run from repository root**

1. **Regenerate SO (Go) bindings:**
   ```bash
   make
   ```

2. **Regenerate SDK (TypeScript) bindings:**
   ```bash
   cd sdks/js/packages/spark-sdk
   mise exec -- yarn generate:proto
   cd - # Return to repo root
   ```

3. **Verify generation:**
   - Check `spark/proto/` for updated Go files
   - Check `sdks/js/packages/spark-sdk/src/spark-wallet/proto-descriptors.ts` for updated TypeScript file

4. **Run code quality checks:**
   ```bash
   cd spark && mise lint && cd -
   cd sdks/js && yarn build && cd -
   ```

## Critical Notes

- MUST use `mise exec --` for TypeScript generation to ensure protoc v29.3
- Never update tool versions without user consent
- If proto generation fails, check mise.toml for correct protoc version

## Usage

After modifying any `.proto` file in the `protos/` directory, run:
```
/proto-regenerate
```

This ensures both Go and TypeScript bindings stay in sync.
