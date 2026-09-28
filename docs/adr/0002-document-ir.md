# ADR 0002 — canonical linear blocks plus outline intervals

Status: accepted.

Every parser outputs one ordered block stream plus a hierarchical outline whose nodes map to half-open block intervals.

Reason: reading order and navigation hierarchy are different dimensions. This handles EPUB chapters spanning files and multiple chapters inside one file without format-specific logic downstream.
