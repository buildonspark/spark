---
description: For a single slack link, diagnose what the root cause may be
---

# Diagnose Slack Errors

Looks up a specific slack message which contains an error/issue, extracts log links, queries OpenSearch logs, looks at the database, analyzes trace data, and diagnoses issues with proposed fixes.

## Prerequisites

Before running this command, ensure the following environment variables are set:

```bash
export SLACK_BOT_TOKEN="xoxb-..."  # Slack bot token with channels:history or groups:history scope
```

**AWS Credentials:**
AWS credentials are required to query OpenSearch logs. If you encounter authentication errors:

```bash
# Refresh AWS credentials (SSO session expired)
source aws-mfa

# May need to set AWS role
export AWS_DEFAULT_ROLE=IMOC
```

This will authenticate you with AWS SSO and set up temporary credentials for accessing production OpenSearch.

**Database Access (Optional):**
Database queries can enhance diagnosis but are optional. When needed:
- Use `./scripts/rds.sh prod` (from SO directory) to get read-only database access
- **NEVER use `-w` flag** (write access)
- **ALWAYS ask user permission** before running any query
- See "Database Access for Investigation" section below for detailed safety guidelines

## Usage

```bash
/diagnose_single_slack_error https://lightsparkgroup.slack.com/archives/C03BFC2KL67/p1765864885835579
```

This command will:
1. Parse the Slack message
2. Query the OpenSearch logs
3. Extract trace_id if present in logs and query for full trace from OpenSearch
4. Check the local error logs (debug/errors/) for previous occurrences (we write this each time this command is run)
5. Use Task agent with subagent_type='Explore' to locate relevant code
6. Read the code and analyze the root cause
7. If needed, look in the database for any additional information that would help to debug the issue
8. Generate a comprehensive diagnosis report with:
   - Error summary
   - Root cause analysis
   - Historical context (from local error logs)
   - Any relevant database information
   - Affected code locations
   - Proposed fix
   - Severity assessment
   - Resolution tips
9. Save the diagnosis to debug/errors/{error_signature}.md
10. Output the diagnosis
11. Post a response to the slack thread

If OpenSearch queries fail due to auth, output the error message and guide the user to refresh AWS credentials with 'source aws-mfa'.

## What This Command Does

### Step 1: Parse the Slack message

Runs the `./scripts/diagnose_slack_errors.sh` script with a single link to a slack thread which contains an error/issue in #alerts-prod-spark.

Searches the Slack output for:

**Log link patterns:**
```
:memo: Logs → https://log.us-west-2.sparkinfra.net/_dashboards/goto/8fe39392be1e352209f6a8b9f878ef33
```

**Error patterns to extract:**
- Error type (e.g., "TransferError", "ValidationError", "TimeoutError")
- Error messages (full text of the error)
- Stack trace signatures
- Handler/function names mentioned
- Key terms (e.g., "insufficient balance", "duplicate transfer", "timeout")

Extracts:
- Environment (dev/loadtest/prod) from URL
- Log dashboard ID/search query
- Searchable error keywords for historical search

### Step 2: Query the OpenSearch logs

**Important:** Must run from the SSP sparkcore directory with `PYTHONPATH=.`

```bash
# Navigate to SSP sparkcore directory
cd /Users/kph/code/lightsparkdev/webdev/sparkcore

# Query logs with error filtering
PYTHONPATH=. uv run python scripts/opensearch-curl.py \
  --env prod \
  --start_time "2025-12-16T02:00:00" \
  --end_time "2025-12-16T02:30:00" \
  --query_string "ERROR_PATTERN AND level:ERROR" \
  --size 100 \
  --stdout > /tmp/error_logs.json
```

**Common Query Patterns:**
```bash
# Query specific error message
--query_string "sync_tree_nodes_coordinator AND level:ERROR"

# Query specific container logs
--match_phrase "kubernetes.container.name||sparknode"

# Combine filters
--query_string "error_pattern" --match_phrase "kubernetes.container.name||sparkcore"
```

Searches for:
- Error messages within the time window
- Associated trace_id fields
- Request context
- Stack traces and exception details

### Step 3: Extract trace_id if present in logs and query for full trace from OpenSearch

From the initial log results:
1. Extract `trace_id` or `spans.trace_id` field
2. Query OpenSearch again with trace_id filter:
```bash
--match_phrase "trace_id||{extracted_trace_id}"
```

This retrieves:
- Full request lifecycle logs
- All related span events
- Error context and stack traces

### Step 4: Check the local error logs (debug/errors/) for previous occurrences (we write this each time this command is run)

Prior logs for issues are saved at:
**File location:** `debug/errors/{error_signature}.md`

Look for prior issues that have the same error signature and see if there are causes that have been diagnosed previously.  Note that this does not mean the issue is the same again, but factor that information into your diagnosis.

### Step 5: Use Task agent with subagent_type='Explore' to locate relevant code

Based on the logs and research from OpenSearch, determine if we need to understand the code better to diagnose the issue.  If we do, use a Task agent with subagent_type'Explore' to locate relevant code.  Note that the code in production may differ from the local code.

### Step 6: Read the code and analyze the root cause

Read the code that was located in Step 5 to determine if there are code issues related to the issue.

### Step 7: If needed, look in the database for any additional information that would help to debug the issue

Utilize the information in the "Database Access for Investigation" section to determine how to access the database.  Query the database for any relevant information that would be helpful to debug the issue.


### Step 8: Generate a comprehensive diagnosis report 

Using the collected data:

**Analyze logs:**
- Identify error messages and stack traces
- Determine which service/handler failed
- Extract relevant parameters and state

**Search codebase:**
- Locate the failing code (SO or SSP) based on service name
- Read the relevant handler/function
- Understand the expected flow vs actual behavior

**Consider context:**
- Check for known issues or patterns
- Review recent changes (if commit info available)
- Analyze timing and frequency

**Analyze database:**
- Identify relevant information in the database and any suspicious patterns or data

**Integrate historical resolutions:**
- Compare current error with historical matches
- Assess if previous fixes apply to this case
- Identify if this is a regression or new variant
- Use past root cause analysis to guide current diagnosis

Generate a diagnosis report containing:

```markdown
## Error Summary
[Brief description of what went wrong]

## Root Cause
[Technical explanation of why it failed]

## Historical Context
[If similar errors found in Slack history:]
- Last seen: [timestamp of previous occurrence]
- Previous resolution: [summary of how it was fixed]
- Relevant PR/commit: [link if available]
- Applicability: [Does the previous fix apply here? Is this a regression?]

[If no similar errors found:]
- This appears to be a new error pattern

## Affected Code
- File: {file_path}:{line_number}
- Function: {function_name}
- Issue: [Specific code problem]

## Proposed Fix
[Detailed fix with code snippets if applicable]
[If leveraging previous fix: "Based on the previous resolution of this issue..."]
[If new issue: "Recommended approach..."]

## Severity
[High/Medium/Low]
[Add: "REGRESSION" flag if this was previously fixed]

## Next Steps
1. [Action items]
2. [...]

## Resolution Tips (for future reference)
- [Key insights about how to diagnose this error]
- [Common causes to check first]
- [Useful debugging commands or queries]
```

### Step 9: Save the diagnosis to debug/errors/{error_signature}.md

After generating the diagnosis, save it to a local knowledge base for future reference.

**File location:** `debug/errors/{error_signature}.md`

**Error signature generation:**
- Create a hash from: error type + handler name + key error message fragments
- Example: `transfer_handler_validation_insufficient_balance_a3f2c1.md`
- Use first 6 chars of MD5 hash for uniqueness

**File contents:**
```markdown
# Error: {Error Type} in {Handler}

**First seen:** {timestamp}
**Last seen:** {timestamp}
**Occurrences:** {count}
**Environments:** {dev/loadtest/prod}

## Error Signature
- Handler: {handler_name}
- Error Type: {error_class}
- Key Message: {normalized_error_message}

## Diagnosis History

### Occurrence {N} - {timestamp}
{Full diagnosis report from Step 7}

---

## Common Causes
- [Updated list based on all occurrences]

## Resolution Patterns
- [Successful fixes that have worked]

## Related Errors
- {link to related error files}
```

**Benefits:**
- Faster diagnosis on repeated errors
- Build institutional knowledge over time
- Track error patterns and trends
- Offline knowledge base (doesn't require Slack API)
- Can be committed to git for team sharing

### Step 10: Output Results

For now, outputs diagnosis to console.

**TODO:** Post back to Slack thread using:
```bash
# Placeholder - implement Slack reply functionality
curl -X POST https://slack.com/api/chat.postMessage \
  -H "Authorization: Bearer ${SLACK_BOT_TOKEN}" \
  -H "Content-type: application/json" \
  --data '{
    "channel": "{channel_id}",
    "thread_ts": "{parent_message_ts}",
    "text": "{diagnosis_report}"
  }'
```

### Step 11: Post a response to the slack thread

Using the same link that was passed into this command and used in the earlier Slack query, executes the script to reply back to the slack thread

```bash
./scripts/reply_to_slack_thread.sh \
     "https://lightsparkgroup.slack.com/archives/C03BFC2KL67/p1765864885835579" \
     "Here’s my response in the thread ✅"
```

The response should contain the information that was found, formatted in the format that Slack messages use. Provide a lot of information in your response. Before doing this, ask the user if they would like you to post the reply. The user may ask for you to iterate on your response before posting it.

## Implementation Instructions

When this command is invoked:

1. **Verify prerequisites:**
   ```bash
   if [[ -z "$SLACK_BOT_TOKEN" ]]; then
     echo "Error: SLACK_BOT_TOKEN environment variable not set"
     echo "Set it with: export SLACK_BOT_TOKEN=\"xoxb-...\""
     exit 1
   fi

   # Check AWS credentials (SSO)
   if ! aws sts get-caller-identity >/dev/null 2>&1; then
     echo "Error: AWS SSO session expired or not authenticated"
     echo "Run: source aws-mfa"
     exit 1
   fi
   echo "✓ AWS credentials are valid"
   ```

2. **Read spark_config.json:**
   ```bash
   SSP_PATH=$(jq -r '.codebase_locations.ssp.path' spark_config.json)
   SO_PATH=$(jq -r '.codebase_locations.so.path' spark_config.json)
   ```

3. **Run Slack fetch script:**
   ```bash

   # Use fetch_slack_channel.sh for one-time fetch
   ./scripts/diagnose_slack_errors.sh {SLACK_URL} > /tmp/slack_errors.md
   ```

4. **Parse log links and extract error patterns from Slack output:**
   ```bash
   # Extract log URLs using grep
   grep -oE "https://log\.(dev\.dev|loadtest\.dev|us-west-2)\.sparkinfra\.net/_dashboards/goto/[a-f0-9]+" /tmp/slack_errors.md

   # Extract error messages/patterns from Slack message text
   # Look for common error indicators:
   # - Lines containing "Error:", "Exception:", "Failed:"
   # - Lines with stack traces
   # - Handler/function names
   ```

5. **For each error message with a log link:**
Note that the link will often return all errors in that category for the past 24 hours, usually in UTC.  To figure out which one is our issue, we will need to look at times slightly before the timestamp of the slack message.
   a. Extract metadata:
   ```bash
   # Get timestamp from Slack message (<!-- ts:1234567890.123456 -->)
   ERROR_TS=$(grep -B2 "https://log" /tmp/slack_errors.md | grep "<!-- ts:" | sed -E 's/.*<!-- ts:([0-9.]+) -->.*/\1/')

   # Get environment from URL
   ENV=$(echo "$LOG_URL" | grep -oE "(dev\.dev|loadtest\.dev|us-west-2)" | sed 's/dev\.dev/dev/; s/loadtest\.dev/loadtest/; s/us-west-2/prod/')

   # Extract error text from Slack message
   ERROR_TEXT=$(grep -A10 "https://log" /tmp/slack_errors.md | grep -v "^#" | grep -v "^--" | head -20)
   ```

   b. Generate error signature and check local error data logs:
   ```bash
   # Extract error components for signature
   # Parse error text to get: error_type, handler_name, key_message
   # Example parsing (adjust based on actual error format):
   ERROR_TYPE=$(echo "$ERROR_TEXT" | grep -oE "(Error|Exception|Timeout|Failure)" | head -1)
   HANDLER_NAME=$(echo "$ERROR_TEXT" | grep -oE "[a-z_]+_handler\.py" | head -1 | sed 's/\.py//')
   KEY_MESSAGE=$(echo "$ERROR_TEXT" | grep -i "error" | head -1 | sed 's/[^a-zA-Z ]//g' | tr ' ' '_' | cut -c1-50)

   # Generate signature hash
   SIG_STRING="${HANDLER_NAME}_${ERROR_TYPE}_${KEY_MESSAGE}"
   SIG_HASH=$(echo "$SIG_STRING" | md5sum | cut -c1-6)
   ERROR_FILE="debug/errors/${HANDLER_NAME}_${ERROR_TYPE}_${SIG_HASH}.md"

   echo "Error signature: $ERROR_FILE"

   # Check if we've seen this error before
   if [[ -f "$ERROR_FILE" ]]; then
     echo "Found previous diagnosis in local error database!"
     echo "Reading: $ERROR_FILE"
     # Show previous diagnosis
     cat "$ERROR_FILE"

     # Ask if user wants to re-diagnose or use cached diagnosis
     # For now, continue with full diagnosis and update the file
   fi
   ```

   c. Query OpenSearch for initial logs:
   ```bash
   # IMPORTANT: Must cd to sparkcore directory and use PYTHONPATH=.
   cd ${SSP_PATH}/sparkcore
   # store the current value
   PYTHONPATH=. uv run python scripts/opensearch-curl.py \
     --env "$ENV" \
     --start_time "$(date -u -r $((${ERROR_TS%.*} - 300)) +"%Y-%m-%dT%H:%M:%S")" \
     --end_time "$(date -u -r $((${ERROR_TS%.*} + 300)) +"%Y-%m-%dT%H:%M:%S")" \
     --query_string "level:ERROR OR level:CRITICAL" \
     --size 100 \
     --stdout > /tmp/initial_logs.json
   cd - >/dev/null  # Return to previous directory
   ```

   d. Extract trace_id:
   ```bash
   TRACE_ID=$(jq -r '.[0]._source.trace_id // .[0]._source.spans.trace_id // empty' /tmp/initial_logs.json | head -1)
   ```

   e. Query by trace_id if found:
   ```bash
   if [[ -n "$TRACE_ID" ]]; then
     cd ${SSP_PATH}/sparkcore
     PYTHONPATH=. uv run python scripts/opensearch-curl.py \
       --env "$ENV" \
       --start_time "$(date -u -r $((${ERROR_TS%.*} - 600)) +"%Y-%m-%dT%H:%M:%S")" \
       --end_time "$(date -u -r $((${ERROR_TS%.*} + 600)) +"%Y-%m-%dT%H:%M:%S")" \
       --match_phrase "trace_id||${TRACE_ID}" \
       --size 1000 \
       --order asc \
       --stdout > /tmp/trace_logs.json
     cd - >/dev/null
   fi
   ```

6. **Analyze logs and diagnose:**

   Read the log JSON files and:

   a. Extract key information:
   - Error messages and stack traces
   - Service name (kubernetes.container.name)
   - Handler/function names
   - Request parameters
   - User context (if available)

   b. Use Task agent with subagent_type='Explore' to locate relevant code:
   ```
   Based on the error in [service] handler [handler_name] with error message "[error]",
   find the relevant code in the codebase
   ```

   c. Read the relevant code files

   d. Determine root cause by analyzing:
   - What the code was trying to do
   - What condition caused the failure
   - Whether it's a code bug, data issue, or external service failure

   e. Incorporate historical context:
   - Review local error database file (if exists)
   - Review Slack history resolutions (if found)
   - Compare current error with historical occurrences
   - Identify if this is a regression
   - Leverage previous fixes when applicable

   f. Propose a fix (if applicable):
   - Code changes needed
   - Configuration updates
   - Data fixes
   - Reference previous fixes if applicable
   - Or mark as "needs investigation" if unclear

7. **Generate diagnosis report:**

   Create a structured report with:
   - Error summary
   - Root cause analysis
   - Historical context (local + Slack)
   - Affected code locations
   - Proposed fix or next steps
   - Severity assessment
   - Resolution tips for future occurrences

7.5. **Save diagnosis to local error logs:**

   ```bash
   # Create debug/errors directory if it doesn't exist
   mkdir -p debug/errors

   # Determine if this is a new error or update to existing
   if [[ -f "$ERROR_FILE" ]]; then
     echo "Updating existing error file: $ERROR_FILE"

     # Read existing file to get first_seen and occurrence count
     FIRST_SEEN=$(grep "^\*\*First seen:\*\*" "$ERROR_FILE" | sed 's/.*: //')
     OCCURRENCE_COUNT=$(grep "^### Occurrence" "$ERROR_FILE" | wc -l)
     NEW_OCCURRENCE=$((OCCURRENCE_COUNT + 1))

     # Append new occurrence to existing file
     cat >> "$ERROR_FILE" <<EOF

---

### Occurrence ${NEW_OCCURRENCE} - $(date -u +"%Y-%m-%d %H:%M:%S UTC")

**Environment:** $ENV
**Trace ID:** $TRACE_ID
**Log Link:** $LOG_URL

$(cat /tmp/diagnosis_report.md)

EOF

     # Update last_seen timestamp
     sed -i.bak "s/^\*\*Last seen:\*\*.*/\*\*Last seen:\*\* $(date -u +"%Y-%m-%d %H:%M:%S UTC")/" "$ERROR_FILE"
     sed -i.bak "s/^\*\*Occurrences:\*\*.*/\*\*Occurrences:\*\* ${NEW_OCCURRENCE}/" "$ERROR_FILE"

   else
     echo "Creating new error file: $ERROR_FILE"

     # Create new error file
     cat > "$ERROR_FILE" <<EOF
# Error: ${ERROR_TYPE} in ${HANDLER_NAME}

**First seen:** $(date -u +"%Y-%m-%d %H:%M:%S UTC")
**Last seen:** $(date -u +"%Y-%m-%d %H:%M:%S UTC")
**Occurrences:** 1
**Environments:** $ENV

## Error Signature
- Handler: ${HANDLER_NAME}
- Error Type: ${ERROR_TYPE}
- Key Message: ${KEY_MESSAGE}
- Signature Hash: ${SIG_HASH}

## Diagnosis History

### Occurrence 1 - $(date -u +"%Y-%m-%d %H:%M:%S UTC")

**Environment:** $ENV
**Trace ID:** $TRACE_ID
**Log Link:** $LOG_URL

$(cat /tmp/diagnosis_report.md)

---

## Common Causes
- [To be populated as more occurrences are analyzed]

## Resolution Patterns
- [To be populated as fixes are applied]

## Related Errors
- [Links to related error files]
EOF
   fi

   echo "Saved diagnosis to: $ERROR_FILE"
   ```

8. **Output results:**

   For now, print to console:
   ```
   ================================
   SLACK ERROR DIAGNOSIS REPORT
   ================================

   Error ID: [log_link_id]
   Timestamp: [error_timestamp]
   Environment: [dev/loadtest/prod]
   Trace ID: [trace_id]

   [Generated diagnosis report]

   ================================

   TODO: Post this report back to Slack thread
   ```

## Error Handling

- **Missing prerequisites:** Check and report missing env vars
- **Script failures:** Show error output and suggest fixes
- **No log links found:** Report that no errors were found in recent messages
- **OpenSearch query failures:** Check AWS credentials and retry
- **No trace_id found:** Analyze available logs without full trace context
- **Code not found:** Report that code location couldn't be determined
- **Multiple errors:** Process each error separately

## Configuration

Default values (can be overridden in the command):

```bash
SLACK_CHANNEL=${SLACK_ERROR_CHANNEL:-"C08RB8T6CNS"}  # Default error channel
TIME_WINDOW=5m  # Look back 5 minutes
ENV_DEFAULT="prod"  # Default to prod if can't determine from URL
MAX_ERRORS_TO_PROCESS=5  # Process up to 5 errors per run
```

## Database Access for Investigation

**⚠️ CRITICAL SAFETY RULES:**
1. **NEVER use `-w` flag** - Read-only access ONLY
2. **ALWAYS ask user permission** before running ANY database query
3. **NEVER run UPDATE, DELETE, INSERT, or any write operations**

### Getting Production Database Access

To query production databases for investigation:

```bash
# Navigate to SO directory
cd /Users/kph/code/spark/spark2/spark
# Or use spark_config.json:
# cd $(jq -r '.codebase_locations.so.path' spark_config.json)

# Get read-only access to production database
# This will prompt for MFA and return a connection string
./scripts/rds.sh prod

# The command outputs a PostgreSQL connection string like:
# postgresql://user:pass@host:port/dbname
```

**IMPORTANT:**
- The connection string from `./scripts/rds.sh prod` provides **read-only** access by default
- **NEVER use `-w` flag** (e.g., `./scripts/rds.sh prod -w`) - this enables write access
- Never request or use write access for diagnosis - read-only is sufficient and safe

### Query Pattern (After Getting User Permission)

**Before running any query, you MUST:**
1. Show the exact SQL query to the user
2. Explain what data it will read
3. Wait for explicit user approval
4. Only then execute the query

**Example workflow:**

```bash
# Step 1: Get user permission
echo "I would like to run the following read-only query to investigate:"
echo "SELECT id, external_spark_id, status, created_at FROM spark_tree_nodes WHERE external_spark_id IN ('...');"
echo ""
echo "This will help identify if these nodes exist in the SSP database and their current status."
echo "May I proceed? (Requires user approval)"

# Step 2: Only after approval, get database access
SO_PATH=$(jq -r '.codebase_locations.so.path' spark_config.json)
cd "$SO_PATH"
DB_URL=$(./scripts/rds.sh prod)
cd - >/dev/null  # Return to previous directory

# Step 3: Run read-only query
psql "${DB_URL}" -c "
  SELECT
    id,
    external_spark_id,
    status,
    network,
    created_at,
    updated_at
  FROM spark_tree_nodes
  WHERE external_spark_id IN (
    '019a800e-12ec-7012-8f40-787d0dea54cb',
    '019aa2d9-0721-7176-bbd9-5bfe5fdfc50c',
    '0199502d-9301-7790-8004-b77561fd6e44'
  )
  LIMIT 100;  -- Always include LIMIT for safety
"
```

### Safe Query Practices

**Always include:**
- `LIMIT` clause to prevent accidentally returning millions of rows
- Specific column names (not `SELECT *`)
- `WHERE` clause to filter results

**Common investigation queries:**

```sql
-- Check if specific nodes exist
SELECT id, external_spark_id, status, created_at
FROM spark_tree_nodes
WHERE external_spark_id IN ('node_id_1', 'node_id_2')
LIMIT 10;

-- Count nodes by status (for aggregate checks)
SELECT status, COUNT(*) as count
FROM spark_tree_nodes
GROUP BY status;

-- Check operator configuration
SELECT id, identifier, host, created_at
FROM spark_signing_operators
ORDER BY created_at
LIMIT 20;
```

**NEVER run:**
```sql
-- ❌ FORBIDDEN - No write operations
UPDATE spark_tree_nodes SET status = 'AVAILABLE';
DELETE FROM spark_tree_nodes WHERE ...;
INSERT INTO spark_tree_nodes ...;
ALTER TABLE ...;
DROP TABLE ...;
TRUNCATE ...;

-- ❌ FORBIDDEN - No queries without LIMIT
SELECT * FROM spark_tree_nodes;  -- Could return millions of rows
```

### Permission Request Template

When you need to query the database, use this template:

```
I need to query the production database to investigate this issue.

Query:
```sql
[EXACT SQL QUERY HERE]
```

Purpose: [Brief explanation of what this will reveal]
Impact: Read-only, returns [estimated number] rows
Safety: Includes LIMIT clause, no write operations

May I proceed with this query?
```

Only execute after receiving explicit user approval.

## Examples

### Example 1: Single Error

```bash
/diagnose_slack_errors
```

Output:
```
Checking Slack for error messages (last 5 minutes)...
Found 1 error message with log link

Processing error 1/1...
- Environment: prod
- Timestamp: 2025-12-15 17:30:45 UTC
- Querying OpenSearch for initial logs...
- Found trace_id: 018c1234-5678-9abc-def0-123456789abc
- Querying full trace...
- Analyzing 47 log entries...
- Located code: ssp/sparkcore/spark/handlers/transfer_handler.py:245

================================
SLACK ERROR DIAGNOSIS REPORT
================================

Error ID: 8fe39392be1e352209f6a8b9f878ef33
Timestamp: 2025-12-15 17:30:45 UTC
Environment: prod
Trace ID: 018c1234-5678-9abc-def0-123456789abc

## Error Summary
Transfer operation failed due to insufficient leaf balance

## Root Cause
The transfer handler attempted to select leaves with total value >= transfer amount,
but the leaf selection algorithm didn't account for leaves that are already locked
by pending operations.

## Historical Context
- Last seen: 2025-12-10 14:23:11 UTC (5 days ago)
- Previous resolution: Applied database-level locking with FOR UPDATE SKIP LOCKED
- Relevant PR: https://github.com/lightsparkdev/webdev/pull/1234
- Applicability: REGRESSION - This fix was deployed but appears to have been reverted or bypassed in a recent change
- Saved diagnosis: debug/errors/transfer_handler_ValidationError_a3f2c1.md (3 occurrences)

## Affected Code
- File: sparkcore/spark/handlers/transfer_handler.py:245
- Function: select_leaves_for_transfer()
- Issue: Race condition - leaves locked between balance check and transfer attempt

## Proposed Fix
Based on the previous resolution of this issue (PR #1234), re-apply database-level locking:

```python
# Current code (missing the lock):
leaves = db.query(Leaf).filter(
    Leaf.user_id == user_id,
    Leaf.status == 'active'
).all()

# Fixed code (restore the lock):
leaves = db.query(Leaf).filter(
    Leaf.user_id == user_id,
    Leaf.status == 'active'
).with_for_update(skip_locked=True).all()  # Skip already locked leaves
```

**Action:** Check recent commits to transfer_handler.py to identify when/why this lock was removed.
Git blame starting from PR #1234 merge date.

## Severity
High - REGRESSION of previously fixed issue, causes user-facing errors

## Next Steps
1. Verify the lock was actually removed by reviewing recent changes
2. If removed, re-apply the fix from PR #1234
3. If not removed, investigate why the lock is not preventing the race condition
4. Add regression test to prevent future removal
5. Deploy fix and monitor error rate
6. Consider code review process improvement to catch regressions

## Resolution Tips (for future reference)
- This is a classic race condition in concurrent transfer handling
- Always use database-level locking (FOR UPDATE) when selecting leaves for transfer
- The SKIP LOCKED clause prevents deadlocks and allows concurrent transfers
- Check for locked leaves before showing "available balance" to users
- Integration tests should include concurrent transfer scenarios
- When this error appears, first check if the database lock is present in the code

================================

TODO: Post this report back to Slack thread

Diagnosis saved to: debug/errors/transfer_handler_ValidationError_a3f2c1.md
```

### Example 2: Recurring Error (Found in Local Database)

```bash
/diagnose_slack_errors
```

Output:
```
Checking Slack for error messages (last 5 minutes)...
Found 1 error message with log link

Processing error 1/1...
- Environment: prod
- Timestamp: 2025-12-15 18:45:12 UTC
- Generating error signature...
- Error signature: debug/errors/lightning_handler_TimeoutError_f7e9b2.md

Found previous diagnosis in local error database!
Reading: debug/errors/lightning_handler_TimeoutError_f7e9b2.md

This error has occurred 7 times previously. Last seen 2 hours ago.
Previous root cause: Lightning Network node unavailable
Previous fix: Increase timeout and add retry logic with exponential backoff

Querying OpenSearch for additional context...
Analyzing logs...

[Full diagnosis follows, incorporating historical knowledge]

Updated occurrence count in: debug/errors/lightning_handler_TimeoutError_f7e9b2.md
```

## Future Enhancements

1. **Slack threading:** Post diagnosis directly to error message thread
2. **Database integration:** Query databases for entity state
3. **Git blame:** Find who last modified the failing code
4. **Similar errors:** Find patterns across multiple errors
5. **Auto-filing:** Create Linear issues for high-severity errors
6. **Metrics:** Track error rates and MTTR improvements
7. **ML classification:** Categorize errors automatically

## Notes

- Diagnosis quality depends on log verbosity and context
- Some errors may require manual investigation
- Always verify proposed fixes before implementing
- The `debug/errors/` directory builds an institutional knowledge base over time
- Error files can be committed to git for team sharing
- Historical context from both Slack and local database helps identify regressions
- The local database is faster than Slack API and works offline
