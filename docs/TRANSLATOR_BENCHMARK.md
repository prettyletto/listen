# Local translator benchmark and attribution

## Selected backend

The first supported local translation pair is English to Brazilian Portuguese
(`en:pt-BR`), using Argos OPUS-MT package 1.9 through CTranslate2 4.8.2 on CPU.
The pinned package is downloaded from
[the Argos model host](https://argos-net.com/v1/translate-en_pb-1_9.argosmodel)
and checked against SHA-256:

```text
1d1cd5e9540c6b38c258bed002a42d3b311b8a189acb74feaa311ef30d175c5b
```

Go starts one long-lived local worker. It loads CTranslate2 once, then accepts
complete JSONL batches; it does not start an inference process per segment.
The managed environment pins CTranslate2 4.8.2 and SentencePiece 0.2.1.

## CPU measurement

The benchmark ran actual inference on the target development machine:

- CPU: AMD Ryzen 7 5825U, 8 physical cores / 16 threads
- Memory: approximately 27 GiB RAM
- Platform: Linux x86_64, CPU-only
- CTranslate2: 4.8.2, `int8_float32`
- SentencePiece: 0.2.1
- Worker threading: 4 intra-threads, 1 inter-thread
- Input: 16 synthetic English segments in batches of four
- Duration estimate: 150 target-language words per minute

The worker was tested with beam sizes 1 and 4. The reported translation ratio
uses estimated target speech seconds divided by translation wall time. The
second ratio also includes the one-time model-load time so the cold start cost
is visible.

| Beam | Model load | Translation | Target words | Estimated audio | Translation ratio | Including load |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 0.082 s | 1.669 s | 440 | 176.0 s | 105.4x | 100.5x |
| 4 | 0.084 s | 2.560 s | 448 | 179.2 s | 70.0x | 67.8x |

Beam 4 is the shipped setting after a qualitative spot check of the synthetic
output. Its small observed output difference does not establish a general
quality advantage; no human-rated or corpus translation evaluation was run.
The benchmark establishes local CPU throughput for this machine and sample,
not a quality guarantee or performance promise for other systems.

The playback planner separately preflights up to four actual opening segments
from the current source. It reports only newly generated translations as fresh
throughput, reuses those outputs through the normal cache, and continues to
update its EMA while translation and speech run. Actual Kokoro WAV durations
calibrate target-language speaking-time estimates. Export has no realtime
deadline.

## Translation smoke check

The real worker loaded the installed model and translated a synthetic sample.
For example:

```text
Hello, this is a local translation check.
-> Olá, é um cheque de tradução local.
```

The word `check` is mistranslated as `cheque` in this context. This is a clear
quality limitation of the selected compact model. The feature is opt-in, and
the setup and playback paths describe it as local translation rather than
claiming human-quality output.

## Attribution and model source

The package README names Jörg Tiedemann and Santhosh Thottingal and cites
“OPUS-MT – Building open translation services for the World,” published in
the 2020 Proceedings of the 22nd Annual Conference of the European Association
for Machine Translation. The package README states that the original OPUS
model from which this package is derived is licensed CC-BY 4.0. See the
[Argos package index](https://github.com/argosopentech/argospm-index/blob/main/index.json),
the [OPUS-MT paper record](https://aclanthology.org/2020.eamt-1.61/), and the
[CTranslate2 CPU hardware documentation](https://opennmt.net/CTranslate2/hardware_support.html)
and [performance guidance](https://opennmt.net/CTranslate2/performance.html).
