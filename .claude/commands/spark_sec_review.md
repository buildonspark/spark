# Spark Flow Security Analysis

You are performing a comprehensive security analysis of a Spark flow. Follow this systematic methodology to identify vulnerabilities that could lead to fund theft, double-spending, or system compromise.

## TARGET FLOW

**Flow to analyze**: {{FLOW_NAME}}
**Primary files**: {{FILE_PATHS}}

---

## PHASE 0: CODEBASE DISCOVERY & ANALYSIS (AUTOMATED FIRST STEP)

🎯 **OBJECTIVE**: Use specialized agents to automatically discover and analyze the codebase to determine what files, components, and patterns need security review.

### Step 0.1: Locate Relevant Components

**Use the Task tool with `subagent_type='codebase-locator'` to find:**

1. **All endpoints for the target flow**:
   - GraphQL mutations in SSP
   - gRPC handlers in SO
   - Client SDK functions
   - Internal SO-to-SO APIs

2. **Database models and operations**:
   - Ent schema definitions
   - Database query patterns
   - Transaction boundaries
   - Locking mechanisms

3. **Authentication/authorization code**:
   - Session validation implementations
   - Identity checks
   - IP-based restrictions

4. **Cryptographic operations**:
   - FROST signature generation
   - Key tweaking/splitting
   - VSS operations
   - Nonce handling

**Example locator prompt:**
```
Find all files involved in the [FLOW_NAME] flow including:
- GraphQL mutations and resolvers
- gRPC handler implementations
- Database schema and query code
- Client SDK functions
- Authentication/authorization checks
- Any cryptographic operations
```

### Step 0.2: Analyze Implementation Details

**Use the Task tool with `subagent_type='codebase-analyzer'` to understand:**

1. **Execution flow patterns**:
   - Entry points and request handling
   - Multi-phase operations (prepare, sign, finalize)
   - SO coordination mechanisms
   - State machine progressions

2. **Data validation patterns**:
   - Where validation occurs (client vs server)
   - Transaction reconstruction vs raw byte acceptance
   - Amount/address verification
   - State transition rules

3. **Database consistency patterns**:
   - Use of `.ForUpdate()` locking
   - Transaction wrapping
   - Race condition prevention
   - Constraint enforcement

4. **Error handling and edge cases**:
   - Partial failure handling
   - Rollback mechanisms
   - Timeout handling
   - Retry logic

**Example analyzer prompt:**
```
Analyze the implementation of [FLOW_NAME] focusing on:
- Complete execution path from client to Bitcoin
- How transactions are constructed (client bytes vs server reconstruction)
- Database locking and atomicity patterns
- Error handling and rollback mechanisms
- Multi-SO coordination and consistency
```

### Step 0.3: Build Security Review Scope

Based on the locator and analyzer results, create:

1. **Complete file inventory** with categorization:
   ```markdown
   ## Files to Review

   ### Entry Points (Highest Priority)
   - SSP: `sparkcore/graphql/mutations/[mutation].py`
   - SO: `spark/so/handler/[handler].go`
   - SDK: `spark-sdk/src/services/[service].ts`

   ### Core Logic
   - [List business logic files]

   ### Database Layer
   - [List schema and query files]

   ### Cryptographic Operations
   - [List crypto-related files]
   ```

2. **Critical code sections** identified by analyzer:
   - Transaction construction points
   - Database operations requiring atomicity
   - Authentication/authorization checkpoints
   - Multi-SO coordination logic

3. **Known patterns to verify**:
   - Raw transaction byte acceptance points
   - Database race condition risks
   - Timelock handling
   - Unilateral exit considerations

### Step 0.4: Document Discovered Architecture

Create flow diagram from analyzer findings:

```markdown
## Discovered Flow Architecture

### Phase 1: [Name]
**Client → SSP**:
- File: `path/to/file.ts:line`
- Function: `functionName()`
- Parameters: [list]
- Validation: [discovered validation]

**SSP → SO**:
- File: `path/to/handler.py:line`
- gRPC Call: `MethodName()`
- Request structure: [details]

**SO Processing**:
- File: `path/to/handler.go:line`
- Database operations: [list]
- Multi-SO coordination: [yes/no]
- Locking strategy: [ForUpdate/transaction/none]

[Continue for all phases discovered]
```

---

## PHASE 1: FUNDAMENTAL SECURITY CONTROLS (MANDATORY SECOND STEP)

🚨 **CRITICAL**: Before analyzing complex vulnerabilities, verify basic security controls exist for EVERY endpoint in the flow.

### For Each Endpoint, Check:

#### 1. Authentication Check
```go
// REQUIRED: Must be present in every user-facing handler
if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
    return err
}
```
- [ ] **gRPC handler**: Has `authz.EnforceSessionIdentityPublicKeyMatches()` call
- [ ] **GraphQL mutation**: Has proper user session validation
- [ ] **Internal endpoint**: Either authenticated OR properly restricted to internal-only access
- ❌ **Missing authentication = CRITICAL vulnerability** (complete fund theft possible)

#### 2. Authorization/Ownership Validation
```go
// REQUIRED: Verify user owns the resources being operated on
if !bytes.Equal(resource.OwnerIdentityPubkey, req.OwnerIdentityPublicKey) {
    return fmt.Errorf("ownership mismatch")
}
```
- [ ] **Node operations**: Verify `node.OwnerIdentityPubkey` matches authenticated user
- [ ] **Transfer operations**: Verify `transfer.SenderIdentityPubkey` or `ReceiverIdentityPubkey` matches
- [ ] **Leaf operations**: Verify leaf ownership through tree/node hierarchy

#### 3. Input Validation
- [ ] **UUIDs**: Proper parsing with error handling
- [ ] **Public keys**: Valid format and length checks
- [ ] **Amounts**: Range validation, overflow protection
- [ ] **Byte arrays**: Length limits, format validation

#### 4. Business Logic Validation
- [ ] **State transitions**: Valid progression (e.g., PENDING → COMPLETED, not reverse)
- [ ] **Resource availability**: Items not already consumed/locked
- [ ] **Preconditions**: Required setup completed before operation

**⚠️ If ANY endpoint is missing authentication/authorization, STOP and report CRITICAL vulnerability before proceeding.**

---

## PHASE 2: ARCHITECTURAL UNDERSTANDING

### Trace Complete Flow (Client → Server → Bitcoin)

1. **Find Client SDK Entry Point**
   - Location: `sdks/js/packages/spark-sdk/src/`
   - Identify: Initial function user calls
   - Document: Parameters and validation

2. **Follow GraphQL/REST API Call to SSP**
   - Location: `sparkcore/spark/handlers/` or `sparkcore/graphql/mutations/`
   - Identify: API mutation/endpoint
   - Document: Request structure and response

3. **Track gRPC Calls to SO**
   - Location: `spark/so/handler/`
   - Identify: SO handler functions
   - Document: Multi-SO coordination patterns

4. **Analyze Bitcoin Transaction Construction**
   - Identify: Who constructs transactions (client vs server)
   - Document: Transaction validation points
   - Check: Raw transaction bytes handling

### Document Flow Phases

Create chronological flow documentation:

```markdown
## Phase 1: [Initial Action]
1. **Client SDK** (file:line):
   - Function: `functionName()`
   - Action: What client does
   - Data sent: What goes over the wire

2. **SSP Handler** (file:line):
   - Function: `handlerName()`
   - Validation: What's checked
   - State changes: Database mutations

3. **SO Handler** (file:line):
   - Function: `handlerName()`
   - Coordination: Multi-SO operations
   - Database: State changes
```

---

## PHASE 3: CRITICAL VULNERABILITY ANALYSIS

### A. Client Data Trust Violations

**FUNDAMENTAL ASSUMPTION: Never trust the SDK or any client-side code**

#### Check for Raw Transaction Bytes Acceptance
```go
// ❌ CRITICAL VULNERABILITY PATTERN:
signingTx, err := common.TxFromRawTxBytes(signingJob.RawTx)  // Direct acceptance
```

**Questions to answer:**
- [ ] Does code accept raw transaction bytes directly from client?
- [ ] Are critical transaction parameters (sequence, outputs, scripts) client-controlled?
- [ ] Is server reconstructing transactions from minimal validated parameters?
- [ ] Are transaction formats validated against expected structures?
- [ ] Could malicious transaction data enable fund theft or business logic bypass?

**Attack scenario template:**
```markdown
1. Attacker crafts transaction with [malicious field]
2. SO accepts raw transaction bytes without validation
3. Attacker achieves [specific exploit]
4. Economic impact: [quantify loss]
```

#### Server-Side Transaction Reconstruction Requirement
```go
// ✅ SECURE PATTERN:
func createRefundTx(userInput UserInput) Transaction {
    // WRONG: tx := parseTransaction(userInput.rawTx)
    // RIGHT: Reconstruct from validated parameters
    tx := NewTransaction()
    tx.AddInput(validateOutpoint(userInput.outpoint))
    tx.AddOutput(validateScript(userInput.script), validateAmount(userInput.amount))
    return tx
}
```

### B. Unilateral Exit Capability Analysis

**🚨 FUNDAMENTAL SPARK ARCHITECTURE PRINCIPLE: Users Can ALWAYS Unilaterally Exit to L1**

Users possess **pre-signed exit transactions** publishable to Bitcoin L1 **at any time** without SO/SSP interaction.

#### Critical Questions for EVERY Flow:

**Timeline Overlap Analysis:**
- [ ] Can user publish exit transactions during flow initiation?
- [ ] Can user publish exit transactions mid-flow?
- [ ] Can user publish exit transactions near completion?
- [ ] Can user publish exit transactions after apparent completion?

**Double-Spending Windows:**
- [ ] Can user receive benefits from both cooperative flow AND unilateral exit?
- [ ] Do unilateral exit timelocks overlap with cooperative flow timing?
- [ ] Can user benefit from both paths simultaneously?

**Resource Commitment:**
- [ ] Do SOs/SSPs commit resources before user funds are cryptographically locked?
- [ ] What happens if user exits L1 after SSP commits but before settlement?
- [ ] Are multi-step operations vulnerable to exit interruption?

**Attack scenario template:**
```markdown
Attack: Double-Spending via Unilateral Exit
Timeline:
1. T+0: User initiates [flow] with [amount]
2. T+30s: SSP/SO commits [resources/payments]
3. T+60s: User publishes unilateral exit transaction
4. T+600s: Exit transaction confirms, user recovers funds
5. Result: User kept [cooperative benefit] + [unilateral exit funds]
Economic impact: [quantify SSP/SO loss]
```

### C. Database Race Conditions & Multi-Step Operations

#### Duplicate Creation Attacks

**Check for Check-Then-Act Gaps:**
```go
// ❌ VULNERABLE PATTERN:
existingEntity, err := db.Entity.Query().Where(...).First(ctx)
if ent.IsNotFound(err) {
    // RACE CONDITION HERE - another call could create between check and create
    db.Entity.Create().Set(...).Save(ctx)
}
```

**Multi-Entity Creation Analysis:**
```markdown
When APIs create Parent → Child relationships, check EACH level:
- [ ] Parent Level: Unique constraint prevents duplicate Parents?
- [ ] Child Level: Unique constraint prevents duplicate Children?
- [ ] Timing Gap: Can multiple calls create same Parent but different Children?
```

**Database Locking Verification:**
```go
// ✅ SECURE PATTERN:
existingTree, err := tx.Tree.Query().Where(...).ForUpdate().Only(ctx)
```
- [ ] Are queries using `.ForUpdate()` to prevent concurrent reads?
- [ ] Are locks held throughout entire multi-step operation?
- [ ] Is operation wrapped in database transaction?

#### Multi-SO Coordination Race Conditions

**Check for Partial SO Failures:**
```go
// ❌ VULNERABLE PATTERN:
for _, so := range signingOperators {
    callSO(so, request) // BAD: No tracking of partial failures
}
db.Operation.Create().SetStatus("COMPLETED") // BAD: Assumes all succeeded
```

**Questions:**
- [ ] What happens if some SOs succeed and others fail?
- [ ] Is there rollback mechanism for partial failures?
- [ ] Can coordinator crash leave inconsistent state?
- [ ] Are operations idempotent for retry?

### D. Timelock & Expiry Vulnerabilities

**Map ALL Timelock Dependencies:**
```markdown
Flow Timelocks:
1. Leaf timelock expiry: [X blocks/time]
2. Transfer expiry: [Y time]
3. Lightning payment timeout: [Z time]
4. SSP quote expiry: [W time]
```

**Critical Timing Attack Analysis:**
- [ ] Can user delay operations until near timelock expiry?
- [ ] Can user exploit gaps between different expiry times?
- [ ] What happens if operation spans timelock expiry?
- [ ] Could one user's timelock expiry harm others?

**Attack scenario template:**
```markdown
Attack: Timelock Expiry Exploitation
Setup: [Multi-step operation with timelocks]
Timeline:
1. T+0: User initiates operation with [X hour] timelock
2. T+(X-1)h: User deliberately delays next step
3. T+Xh: Timelock expires, user can [exploit]
4. Result: [Specific advantage gained]
```

### E. Value Manipulation & Economic Attacks

**Blockchain Value vs Client Value:**
- [ ] Does code always use blockchain-verified values?
- [ ] Can users manipulate amounts, addresses, or parameters?
- [ ] Are there validation steps comparing client data vs blockchain data?

**Fee Quote Gaming:**
- [ ] Are quotes bound to specific operations/parameters?
- [ ] Do quotes have expiration times?
- [ ] Can users reuse quotes across different scenarios?
- [ ] Can users collect quotes during low fees, use during spikes?

### F. FROST/VSS Cryptographic Vulnerabilities

**Nonce Reuse Risks:**
- [ ] Can clients influence SO nonce generation/selection?
- [ ] Are nonces properly consumed and marked as used?
- [ ] Is there atomic nonce allocation?
- [ ] Do clients provide commitments that could be replayed?

**Adaptor Signature Validation:**
- [ ] Are adaptor public keys validated?
- [ ] Is there proof of possession for adaptor keys?
- [ ] Can adaptor keys be manipulated?

**VSS Share Validation:**
- [ ] Are VSS proofs validated?
- [ ] Is threshold reasonableness checked?
- [ ] Are shares validated for correct polynomial?

---

## PHASE 4: ATTACK SCENARIO MODELING

For each potential vulnerability found, create detailed attack scenario:

### Attack Template

```markdown
## Attack: [Descriptive Name]

**Severity**: [CRITICAL/HIGH/MEDIUM/LOW]
**Exploitability**: [HIGH/MEDIUM/LOW]
**Impact**: [Specific monetary/system impact]

### Prerequisites
- Attacker needs: [specific capabilities]
- System state: [required conditions]

### Attack Steps
1. **Attacker action**: [specific API call with parameters]
   **System response**: [code execution, database changes]
   **Code location**: [file:line]

2. **Concurrent action**: [if race condition]
   **System response**: [how system handles concurrency]
   **Code location**: [file:line]

3. **Result**: [final state, money gained/lost]

### Economic Impact
- Attacker gains: [quantify]
- Victim loses: [quantify]
- Per-attack profit: [calculate]

### Code Evidence
```go
// Quote actual vulnerable code with line numbers
```

### Fix Required
```go
// Show specific fix needed
```
```

---

## PHASE 5: VERIFICATION & EVIDENCE GATHERING

### Mandatory Verification Checklist

Before claiming ANY vulnerability, verify:

#### Code Existence Verification
- [ ] Function exists: `rg "func.*FunctionName"` confirms function exists
- [ ] Line-by-line reading: Read actual implementation, not assumptions
- [ ] Multiple file search: Check for validation in other files/phases
- [ ] Database operations: Verify atomic operations and locking

#### Flow Tracing Verification
- [ ] Complete execution path: Traced from API entry through all phases
- [ ] Multi-phase validation: Checked validation in prepare, sign, finalize
- [ ] SO coordination: Understand multi-SO coordination and validation
- [ ] Database transactions: Verified atomicity and isolation

#### Attack Vector Verification
- [ ] Prerequisites confirmed: Attacker can actually reach vulnerable code
- [ ] Exploit steps verified: Each step of attack scenario confirmed
- [ ] Impact assessment: Actual impact matches claimed severity
- [ ] Mitigation check: Existing mitigations don't prevent attack

### Evidence Collection

For each finding, document:

1. **Exact code location**: `file_path:line_number`
2. **Code quote**: Actual vulnerable code snippet
3. **Execution trace**: Complete path through codebase
4. **Attack proof**: Step-by-step demonstration
5. **Economic impact**: Calculated monetary loss

---

## PHASE 6: SEVERITY ASSESSMENT

### Severity Ratings (Be Conservative - Match to Confirmed Impact)

**CRITICAL**:
- ✅ Confirmed fund theft possible (with proof)
- ✅ Complete authentication bypass (verified)
- ✅ System-wide compromise (demonstrated)
- ❌ NOT: Theoretical issues without confirmed exploit

**HIGH**:
- ✅ Significant operational impact (proven)
- ✅ Partial fund loss scenarios (quantified)
- ✅ Privacy breaches (demonstrated)
- ❌ NOT: Minor issues with difficult exploitation

**MEDIUM**:
- ✅ Limited impact with specific conditions
- ✅ Resource exhaustion (DoS)
- ✅ Information disclosure (minor)

**LOW**:
- ✅ Minor issues with minimal impact
- ✅ Code quality issues
- ✅ Best practice violations

### Severity Justification Template

```markdown
**Severity: [LEVEL]**
**Justification**:
- Confirmed exploit path: [yes/no with evidence]
- Economic impact: [specific amount]
- Attack complexity: [high/medium/low]
- Prerequisites: [list required conditions]
- Existing mitigations: [what prevents/reduces impact]
```

---

## DELIVERABLE FORMAT

### Security Analysis Report Structure

```markdown
# [Flow Name] Security Analysis

## Executive Summary
- Flow purpose: [brief description]
- Analysis date: [date]
- Critical findings: [count by severity]
- Overall risk level: [CRITICAL/HIGH/MEDIUM/LOW]

## Flow Overview
- Primary functions: [list with file:line]
- User journey: [step-by-step]
- System components: [SSP, SO, Client]

## Authentication & Authorization Analysis (Phase 1)
[Results of fundamental security controls check]

### Endpoint Security Matrix
| Endpoint | File:Line | Authentication | Authorization | Status |
|----------|-----------|----------------|---------------|--------|
| [name]   | [file:line] | ✅/❌         | ✅/❌         | SECURE/VULN |

## Architectural Flow Analysis (Phase 2)
[Complete flow documentation with phases]

## Critical Vulnerabilities (Phase 3+)

### CRITICAL: [Vulnerability Name]
**Severity**: CRITICAL | **Impact**: [specific impact]
**Location**: `file:line`

#### Vulnerability Description
[Clear explanation of the issue]

#### Attack Scenario
[Detailed step-by-step attack]

#### Code Evidence
```go
// Vulnerable code with line numbers
```

#### Economic Impact
- Attacker gain: [amount]
- Victim loss: [amount]

#### Recommended Fix
```go
// Specific fix with code
```

[Repeat for each vulnerability by severity]

## Security Recommendations

### Immediate Critical Fixes (Required Before Production)
1. [Fix with priority]
2. [Fix with priority]

### High Priority Fixes (Required Soon)
1. [Fix]
2. [Fix]

### Medium/Low Priority Improvements
1. [Improvement]
2. [Improvement]

## Conclusion
[Overall assessment and final recommendations]
```

---

## CRITICAL SUCCESS FACTORS

### Primary Success Factor: VERIFICATION FIRST
**NEVER make security claims without line-by-line code verification**
- Good: "Let me verify this function exists: `rg 'func.*ValidateBalance'`"
- Bad: "This validation appears to be missing based on API review"

### Secondary Success Factor: FUNDAMENTALS FIRST
**Always check authentication/authorization before complex vulnerabilities**
- The most critical vulnerabilities are often the simplest (missing auth)
- Complete Phase 1 before moving to Phase 2+

### Tertiary Success Factor: PROPORTIONAL SEVERITY
**Match severity to confirmed impact**
- Good: "HIGH - Users can exploit timing window for $X gain"
- Bad: "CATASTROPHIC - Complete system compromise" (without proof)

### Methodology Principles

❌ **NEVER**:
- Skip Phase 0 automated discovery - always use codebase agents first
- Claim vulnerabilities without line-by-line verification
- Use "CRITICAL" without confirmed exploit path
- Assume validation is missing without systematic search
- Skip authentication checks on any endpoint
- Make claims about database behavior without checking locking
- Manually search for files when agents can do it better

✅ **ALWAYS**:
- Begin with Phase 0: Use codebase-locator to find all relevant files
- Use codebase-analyzer to understand implementation patterns
- Let agents do discovery work - they're faster and more thorough
- Use verification checklist before making claims
- Check fundamentals (auth/authz) first
- Trace complete execution paths
- Verify database atomicity and constraints
- Confirm attack prerequisites and exploitability
- Match severity to confirmed impact
- Consider unilateral exit capability
- Document with file:line references

---

## ANALYSIS EXECUTION

Now begin the security analysis of the specified flow following this methodology:

1. **Start with Phase 0**: Use codebase-locator and codebase-analyzer agents to discover components and understand implementation
2. **Continue with Phase 1**: Check authentication/authorization on ALL endpoints discovered in Phase 0
3. **Document flow**: Create complete architectural flow diagram from Phase 0 findings
4. **Identify vulnerabilities**: Systematically check each category using Phase 0 insights
5. **Model attacks**: Create detailed attack scenarios with evidence
6. **Verify everything**: Use checklist before claiming any vulnerability
7. **Assess severity**: Conservative ratings matching confirmed impact
8. **Provide fixes**: Specific code changes required

**Remember**:
- Use the Task tool with specialized agents in Phase 0 - don't manually search for files
- Accuracy and usefulness over dramatic findings
- The goal is to find real, exploitable vulnerabilities with confirmed impact, not theoretical issues
- Let the agents do the heavy lifting of discovery and analysis before diving into security review

Begin analysis now with Phase 0.
