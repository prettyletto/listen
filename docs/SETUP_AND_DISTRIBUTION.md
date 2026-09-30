# Setup and distribution

## Goal

The repository can be technically complex; installation cannot be.

Desired eventual experience:

```sh
# package-manager or installer
listen setup
listen book.epub
```

## Managed runtime

`listen setup` provisions a private, uv-managed CPython 3.13 environment at
`$XDG_DATA_HOME/listen/runtime/venv`. The supported first target is Linux
x86_64. Install `uv` once using its [official installation instructions](https://docs.astral.sh/uv/getting-started/installation/).
The user does not need to activate a virtual environment or choose a Python
version.

The setup command syncs the complete dependency lock for Kokoro 0.9.4,
English G2P model 3.8.0, CTranslate2 4.8.2, and SentencePiece 0.2.1, including
pinned transitive packages and SHA-256 hashes. It selects CPU PyTorch. In an
interactive terminal, setup guides the user through one or more listening
languages, a default voice for each, and the optional local translation
install. Pressing Enter keeps the displayed defaults. Non-interactive setup
uses saved or default choices. The selected voice pipelines are loaded and
health-checked before config is saved. Voice and speech-model files are cached
beneath `$XDG_CACHE_HOME/listen/huggingface`.

Local translation is opt-in. Run `listen setup --translate en:pt-BR` to ensure
the speech runtime, configure and health-check a Brazilian Portuguese voice,
download the pinned Argos OPUS-MT package, and start it once as a health check.
The model archive is verified against its committed SHA-256 before extraction.
The model is stored beneath `$XDG_DATA_HOME/listen/translation`; it does not
contain user text. The current translation installer supports English to
Brazilian Portuguese only.

## PDF reading

PDFs are parsed on a best-effort basis and require Poppler's
`pdftohtml` and `pdfimages` commands. Poppler also provides `pdfinfo`, which
supplies available title and author metadata. On Arch Linux, install Poppler with
`sudo pacman -S poppler`. `listen setup` does not download or install PDF tools.

When `qpdf` is installed, its PDF outline is used for bookmark navigation.
Without it, headings are inferred from page layout and a warning is retained
with the parsed document. Pages that appear scanned are rendered with Poppler
and recognized by local Tesseract using TSV output and page geometry. Install
Tesseract plus trained data for the desired languages separately; Listen does
not download language packs. English is the default. Configure additional
language IDs, for example `listen config set ocr-languages eng+por`. Listen
checks that each configured pack is installed before OCR begins. See the
[Tesseract installation guide](https://tesseract-ocr.github.io/tessdoc/Installation.html)
and [command-line guide](https://tesseract-ocr.github.io/tessdoc/Command-Line-Usage.html).

For normal `listen scanned.pdf` playback, pages are OCRed sequentially and
their segments enter the ordered speech pipeline as each page completes. The
opening playback buffer is capped at three seconds for this path, and a short
first page starts playback as soon as that page's audio is ready. OCR then
continues while the listener hears earlier pages. The terminal/player reports
recognized-page progress. The live pass cannot offer PDF chapter selection
until the full outline is known. `--no-play`, `--export`, and translated
reading process all scan-like pages before audio generation or playback.

OCR is local: PDF pages and recognized text are not uploaded. Atomic page
artifacts are keyed by document content, OCR/parser versions, Tesseract
version, language list, resolution, and page number. Interrupted runs reuse
completed pages and completed speech audio. A stopped streamed PDF starts from
the first page on the next invocation; its cached work is reused, but the
playback position is not restored yet. Confidence below the current threshold
and pages with no recognized words produce warnings. Recognition quality
depends on language data, print/scan quality, rotation, and layout. The only
committed OCR quality fixture currently covers English, so other languages
are configurable but not yet claimed as verified.

To bound local processing of untrusted documents, PDF input is limited to
256 MiB and 5,000 pages. Extractor output is capped and each external PDF tool
has a two-minute time limit. Individual OCR raster images and Tesseract output
also have size limits.

## Cloud translation

The direct OpenAI adapter is optional and has no setup download. Set
`OPENAI_API_KEY` in the environment that runs `listen`. Configure the policy
with `listen config set cloud-policy <never|ask|allowed>` and select a model
with `listen models` followed by `listen config set cloud-model <model-id>`.
`listen config` never stores credentials. `ask` is the default and declines
when no interactive terminal is available; `allowed` can switch automatically
after an unsustainable local startup probe or when the local model is absent.
Document text is sent only after the policy authorizes this operation.

`internal/setup/requirements.in` lists direct worker dependencies.
`internal/setup/requirements.lock` locks the resolved CPython 3.13 Linux x86_64
CPU environment for speech and local translation. To refresh that lock
intentionally, run:

```sh
uv pip compile \
  --python-version 3.13 \
  --python-platform x86_64-unknown-linux-gnu \
  --torch-backend cpu \
  --index https://download.pytorch.org/whl/cpu \
  --generate-hashes \
  --output-file internal/setup/requirements.lock \
  internal/setup/requirements.in
```

Setup installs from the committed lock with `uv pip sync --index
https://download.pytorch.org/whl/cpu --require-hashes --strict`, so a repeat
setup restores the same dependency graph and rejects artifacts that do not
match its hashes.

Setup is repeatable. If the runtime directory is removed or an earlier setup
was interrupted, run `listen setup` again to restore it. Configuration is
saved only after the synthesis check succeeds.

## Setup state machine

1. Check host architecture/OS.
2. Check mpv/ffmpeg availability.
3. Provision or reuse the private Python runtime/environment.
4. Sync hash-locked speech and translation worker dependencies.
5. In an interactive terminal, select listening languages and default voices;
   preserve saved/default choices without a terminal.
6. Offer local English-to-Brazilian-Portuguese translation unless the user
   supplied `--translate en:pt-BR` explicitly.
7. Synthesize a short local health sample for every configured voice.
8. If local translation was selected, download and verify that model, start
   its worker, and check its language-pair capability.
9. Save setup preferences only after the requested health checks pass.
10. Before translated playback, measure viability using opening segments of
    that source; setup does not upload sample or source text.

## Doctor

`listen doctor` prints platform and XDG paths, uv/mpv/ffmpeg versions,
configuration status, the managed Python/Kokoro version, the selected voice,
speech-model cache state, and whether the optional local translator is ready.
Keep these details out of ordinary playback.

## Packaging

Initially prioritize Linux x86_64. Do not block architecture design on multi-platform packaging, but keep path/process abstractions portable enough for later macOS/Windows work.
