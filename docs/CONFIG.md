# Configuration model

The user configuration is human-readable JSON at
`$XDG_CONFIG_HOME/listen/config.json`. Expose mutations through `listen
config` so users do not need to hand-edit it. Writes replace the full file
atomically with owner-only permissions.

Current schema:

```json
{
  "config_version": 1,
  "setup_complete": false,
  "speech": {
    "default_voice": "af_heart",
    "default_speed": 1.0
  },
  "voices": {},
  "cloud_policy": "ask",
  "cloud_provider": "openai",
  "cloud_model": "gpt-5.6-luna"
}
```

`listen config` displays the file. `listen config set voice <id>` changes the
default voice; `listen config set voice <locale> <id>` records a voice for a
language. `listen config set speed <0.5–2.0>` and `listen config set
cloud-policy <ask|never|allowed>` update the matching preferences.
`listen config set cloud-provider openai` and `listen config set cloud-model
<model-id>` select the direct translation adapter. `listen models` lists model
IDs using `OPENAI_API_KEY`; model discovery sends no source text.

The selected speech voice and speed apply to normal listening. One-shot
`--voice` and `--speed` options take precedence without changing the saved
configuration. Cloud policy governs source-text translation: `never` keeps all
translation local, `ask` requires an interactive approval before a cloud
request, and `allowed` lets the configured provider take over automatically
when the opening local playback probe is unsustainable or local translation
is unavailable. The provider and model are pinned for the full operation so
cache and resume identity cannot change mid-book. The API key is read from
`OPENAI_API_KEY`; it is never stored in this file.

Per-invocation flags override config but do not mutate it unless explicitly using `listen config`.
