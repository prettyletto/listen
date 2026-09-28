# ADR 0001 — Go core, Python Kokoro worker

Status: accepted.

Go owns product orchestration and parsing. Kokoro remains behind a long-lived Python worker protocol. This preserves the proven TTS ecosystem without making the entire application a Python dependency graph.

Consequences: setup must manage Python privately; worker protocol must be versioned/tested; replacing Kokoro implementation later does not affect core packages.
