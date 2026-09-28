# Generic translator runner protocol v1

Optional advanced integration for external CLIs/agents. Prefer direct API/local-runtime adapters when available.

Transport: newline-delimited JSON over stdin/stdout for a long-lived runner when possible.

Request:

```json
{
  "type":"translate",
  "request_id":"tr-7",
  "source_language":"en",
  "target_language":"pt-BR",
  "style":"faithful natural book translation; do not summarize",
  "glossary":{"Acme":"Acme"},
  "segments":[
    {"id":"s100","text":"..."},
    {"id":"s101","text":"..."}
  ]
}
```

Response:

```json
{
  "type":"translated",
  "request_id":"tr-7",
  "segments":[
    {"id":"s100","text":"..."},
    {"id":"s101","text":"..."}
  ]
}
```

Rules:

- every input ID appears exactly once in success response;
- no extra IDs;
- no markdown fences around protocol output;
- response order is irrelevant because Go maps by ID;
- missing/duplicate IDs invalidate the whole batch;
- partial token streams may be consumed internally but are not committed as results;
- stderr is diagnostic only.
