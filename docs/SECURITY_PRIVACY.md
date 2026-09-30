# Security and privacy

## Remote content

The strongest rule: source text is never sent to a remote backend unless cloud
use is explicitly allowed for that operation/account configuration. The
`never` policy and a declined or unavailable `ask` prompt prevent construction
of the cloud adapter, so the translation pipeline cannot make the request.
`allowed` authorizes the configured OpenAI translator when the local opening
playback probe classifies translation as unsustainable or a local translator
is unavailable. Model metadata discovery sends no source text.

PDF OCR is local and invokes Poppler and Tesseract subprocesses. Page images,
recognized text, and OCR cache artifacts stay on the device; OCR never uses the
cloud-translation policy or contacts a recognition service.

## Untrusted documents

Treat EPUB/HTML/PDF as untrusted input.

- prevent ZIP path traversal when opening EPUB;
- cap decompressed EPUB resource sizes and total archive expansion;
- never execute embedded scripts;
- HTML parsing is data-only;
- apply parser resource limits to hostile PDFs;
- never use source-provided filenames directly for cache/export paths without sanitization;
- do not shell-interpolate document text into subprocess commands.

## Worker paths

Go chooses output paths. Python worker must reject outputs outside the provided cache root.

## Credentials

The direct OpenAI adapter reads `OPENAI_API_KEY` from the environment and sends
it only as a Bearer authorization header. The key is not part of settings,
session manifests, cache identities, or logs. The Responses request sets
`store:false` and does not use background mode. This avoids application-state
storage by that request; the provider's applicable service and retention terms
still govern the remote processing.

## Logs

Default logs must not dump full source text or translations. Verbose diagnostics should still avoid secrets and API credentials.
