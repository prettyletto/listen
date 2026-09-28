# Intended package map

This is a target map, not an instruction to create empty abstraction files before a slice needs them.

```text
cmd/listen/                 executable
internal/app/               command dispatch and composition root
internal/domain/            format-independent document IR
internal/source/            detection + parser registry
internal/source/epub/       EPUB container/package/nav/XHTML adapter
internal/source/markdown/   Goldmark -> IR
internal/source/text/       scored plain-text structure
internal/source/html/       generic article HTML
internal/source/pdf/        PDF best-effort parser
internal/structure/         outline validation/roles/major units
internal/segment/           semantic segment planning
internal/normalize/         speech-oriented transforms
internal/translate/         interface/planner/cache
internal/translate/local/   local adapters
internal/translate/cloud/   direct remote adapters
internal/translate/runner/  generic agent/CLI adapters
internal/speech/            synthesizer interface
internal/speech/kokoro/     Python-worker client
internal/pipeline/          queues, workers, reorder, backpressure
internal/playback/          mpv JSON IPC
internal/export/            ffmpeg/M4B/chapter artifacts
internal/cache/             XDG paths/content-addressed stores
internal/session/           resume state
internal/settings/          validated user config and voice preferences
internal/setup/             managed runtime/models/voices
internal/ui/                concise terminal UI
internal/speech/kokoro/     Kokoro client, voice registry, embedded worker
contracts/                  cross-process protocol specs
testdata/                   golden parser corpus
reference/python-prototype/ original prototype
```

Dependency direction should trend inward toward `domain`; provider packages must not be imported by domain/parser packages.
