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
pinned transitive packages and SHA-256 hashes. It selects CPU PyTorch, asks for
a listening language, configures its default voice, and runs a short local
synthesis check. Only configured language pipelines are loaded by setup.
Voice and speech-model files are cached beneath
`$XDG_CACHE_HOME/listen/huggingface`.

Local translation is opt-in. Run `listen setup --translate en:pt-BR` to ensure
the speech runtime, configure and health-check a Brazilian Portuguese voice,
download the pinned Argos OPUS-MT package, and start it once as a health check.
The model archive is verified against its committed SHA-256 before extraction.
The model is stored beneath `$XDG_DATA_HOME/listen/translation`; it does not
contain user text. The current translation installer supports English to
Brazilian Portuguese only.

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
3. Provision compatible Python runtime/environment if needed.
4. Sync hash-locked speech and translation worker dependencies.
5. Ask listening languages.
6. Download/verify required models/voices.
7. Synthesize a short local health sample.
8. If `--translate en:pt-BR` was supplied, verify/download that local model,
   start its worker, and check its pair capability.
9. Save setup state only after the requested health checks pass.

## Doctor

`listen doctor` prints platform and XDG paths, uv/mpv/ffmpeg versions,
configuration status, the managed Python/Kokoro version, the selected voice,
speech-model cache state, and whether the optional local translator is ready.
Keep these details out of ordinary playback.

## Packaging

Initially prioritize Linux x86_64. Do not block architecture design on multi-platform packaging, but keep path/process abstractions portable enough for later macOS/Windows work.
