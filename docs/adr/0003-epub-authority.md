# ADR 0003 — EPUB spine and navigation are authoritative

Status: accepted.

Use OPF spine for canonical reading order and EPUB navigation/NCX for hierarchy. XHTML headings and text heuristics are fallback/repair evidence only.

Reason: EPUB explicitly models reading order and navigation; regex-based chapter inference discards higher-quality source information.
