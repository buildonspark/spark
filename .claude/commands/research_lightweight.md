Research and deeply understand the entire **$ARGUMENTS** flow/feature in the codebase.

## Execution Strategy

**IMPORTANT**: You MUST use the Task tool with `subagent_type="Explore"` to launch parallel subagents for each research area below. Launch ALL of these subagents in parallel (in a single message with multiple Task tool calls) to maximize efficiency:

1. **SDK Subagent** - Explore `sdks/js/` and `sdks/rs/` for SDK usage of "$ARGUMENTS":
   - Public API methods and their parameters
   - How clients call this feature
   - TypeScript/Rust type definitions

2. **GRPC/Proto Subagent** - Explore the GRPC layer for "$ARGUMENTS":
   - Proto definitions in `protos/`
   - GRPC handlers in `spark/so/grpc/`
   - Service contracts and message types

3. **Handler Subagent** - Explore `spark/so/handler/` for "$ARGUMENTS" business logic:
   - Request handlers and their implementations
   - Helper functions and utilities in `spark/so/helper/`
   - Validation and error handling

4. **Data Layer Subagent** - Explore the data layer for "$ARGUMENTS":
   - Database entities in `spark/so/ent/schema/`
   - State transitions and status changes
   - Database queries and mutations

5. **Inter-Service Subagent** - Explore inter-service communication for "$ARGUMENTS":
   - Gossip messages and protocols
   - Internal service calls between operators
   - DKG or signing coordination in `spark/so/dkg/`

Each subagent prompt should instruct it to be "very thorough" and return:
- All relevant file paths with line numbers
- Function/method signatures
- Key code snippets showing the logic
- How data flows through that layer

## Aggregation

After all subagents complete, synthesize their findings into a comprehensive report organized by layer.

## Expected Output

Return a comprehensive report of the entire flow containing:

- **Overview**: High-level summary of what this feature does
- **Entry Points**: SDK methods and GRPC endpoints (with file paths and line numbers)
- **Function Call Chain**: Complete trace from SDK → GRPC → Handler → Database/External Services
- **Key Functions**: List of important functions with their responsibilities and signatures. Explain the purpose of each function within the greater flow.
- **Database Interactions**: Entities read/written and their relationships
- **State Machine**: Any status transitions or state changes
- **Error Handling**: How errors are propagated and handled
- **Related Features**: Dependencies on or connections to other features

**Style**: The report must be **concise but thorough**. Include all necessary technical details (file paths, function names, signatures, key logic) but avoid fluff, filler, or unnecessary prose. Be direct and information-dense. Every line should provide value.
