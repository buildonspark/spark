Search Linear issues by label and/or project and determine how likely we can fix it.
Note that the user must do this before running the command:

Get a Linear API key
- Linear → **Settings**
- **Account → Security & Access**
- **Create API key**
- Scope: **Read-only**
- Copy the key

```bash
export LINEAR_API_KEY="lin_api_..."
```

Usage examples:
- /linear --label "Bug"
- /linear --project "Spark"
- /linear --label "Bug" --project "Spark" --limit 25
- /linear --team-key "SPARK" --state-type "triage"

Return information:
   - The level of complexity of fixing this issue from 0 to 100 with 100 being the most complex
   - The level of certainty that you have that you could correctly and completely fix this issue from 0 to 100 with 100 being the most certain
   - The information on what the issue is, along with code pointers to what needs to be fixed and suggestions for how to fix it

Instructions:
Before running anything, check if environment variable LINEAR_API_KEY is set.  If not, return that the user needs to set this value first.
1) Get today's date as {date}. Run: ./scripts/linear_query.sh {{args}} > thoughts/shared/{date}/linear_issues.json
2) Read thoughts/shared/{date}/linear_issues.json
3) For each issue:
   - Determine if there is enough context in the issue to figure out what it's asking for.  If not, set the level of certainty for it to 0 and output for that issue that there isn't enough context.  Set complexity to 100 also.
   - If there is enough context that it's worth researching more:
      - Use the create_plan agent to create a plan to fix this issue.  Ask it to create a plan for how to fix the issue.  Wait until the agent returns data.
      - Read the output from the created plan to determine how complex the fix is and how certain you are that you can correctly and completely fix it
      - Write up the specified information as per what I listed in "Return information". Put this into a new file for each issue under thoughts/shared/{date}/
4) Once you have done step 3 for every issue returned by the query, create a single markdown file in thoughts/shared/{date}/linear_issue_summary.md that orders the issues by the level of certainty. For each issue, give a short summary of the issue and the fix.  This should only be a short paragraph. Also include the path to the full description file. 
