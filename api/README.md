# Contracts shared between services

`usage-event.example.json` is the value of every record on the `llm.usage` topic (key: `app_id` as a string).
The gateway produces it; fl-ledger and fl-guard consume it. Each service keeps its own Go type, and each has a
test that parses this file and fails if a field is added, removed or renamed, so the three cannot drift apart
silently. Change this file first, then the producers and consumers.
