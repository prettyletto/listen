# Test strategy

## Layers

### Pure unit tests

- outline interval derivation;
- role classification;
- segment planning;
- reorder gates;
- cache keys;
- realtime ratio/planner logic;
- config/cloud policy.

### Golden parser tests

Each source fixture has expected `Document` structure JSON. Compare semantic structure, not irrelevant parser object details.

### Protocol tests

Use fake long-lived workers that intentionally:

- return out of order;
- fail one request;
- emit malformed JSON;
- terminate unexpectedly;
- delay until cancellation.

### Pipeline integration

No real ML required. Fake translator and fake TTS produce deterministic text/audio metadata. Assert:

- playback append order;
- backpressure;
- cancellation;
- resume;
- cache reuse;
- provider switch only at allowed boundary.

### Real-model opt-in tests

Tagged/integration scripts may verify Kokoro and chosen local translators on developer machines. They are not required in normal CI.

## Parser invariants

For every fixture:

- contiguous block sequence;
- valid outline ranges;
- children contained by parents;
- no invalid sibling overlap;
- deterministic output across repeated parses;
- no code-fence fake Markdown headings;
- no EPUB TOC duplication in default narration;
- explicit nav order never overrides spine reading order.

## Fuzzing

Good fuzz targets:

- plain-text heading classifier;
- Markdown-to-block conversion;
- HTML fragment/anchor resolver;
- JSONL worker protocol decoder;
- reorder gate duplicate/huge sequence handling.
