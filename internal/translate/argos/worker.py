#!/usr/bin/env python3
"""Long-lived JSONL CTranslate2 worker for the pinned Argos OPUS-MT model."""

import argparse
import json
import sys
import traceback

PROTOCOL_VERSION = 1
ENGINE_ID = "argos-opus-en-pb-1.9-ct2-4.8.2"
MAX_LINE_BYTES = 20 * 1024 * 1024
MAX_SEGMENTS = 16
MAX_TEXT_BYTES = 1024 * 1024


def emit(event):
    sys.stdout.write(json.dumps(event, ensure_ascii=False, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def load_runtime(model_dir):
    import ctranslate2
    import sentencepiece

    translator = ctranslate2.Translator(
        str(model_dir / "model"),
        device="cpu",
        compute_type="int8_float32",
        inter_threads=1,
        intra_threads=4,
    )
    tokenizer = sentencepiece.SentencePieceProcessor(model_file=str(model_dir / "sentencepiece.model"))
    return translator, tokenizer


def translate_batch(translator, tokenizer, segments):
    tokens = [tokenizer.encode(item["text"], out_type=str) for item in segments]
    outputs = translator.translate_batch(
        tokens,
        batch_type="tokens",
        max_batch_size=4,
        beam_size=4,
        length_penalty=0.2,
        replace_unknowns=True,
    )
    if len(outputs) != len(segments):
        raise ValueError("unexpected model result count")
    translated = []
    for source, output in zip(segments, outputs):
        if not output.hypotheses:
            raise ValueError("model returned no translation")
        text = tokenizer.decode(output.hypotheses[0]).replace("▁", " ").replace("_", " ")
        text = " ".join(text.split())
        if not text:
            raise ValueError("model returned empty text")
        translated.append({"segment_id": source["segment_id"], "text": text})
    return translated


def validate_request(request):
    if request.get("type") != "translate":
        raise ValueError("unexpected request type")
    if request.get("target_language") != "pt-BR":
        raise ValueError("unsupported target language")
    segments = request.get("segments")
    if not isinstance(segments, list) or not 1 <= len(segments) <= MAX_SEGMENTS:
        raise ValueError("invalid translation batch size")
    seen = set()
    for item in segments:
        if not isinstance(item, dict):
            raise ValueError("invalid segment")
        segment_id = item.get("segment_id")
        text = item.get("text")
        if not isinstance(segment_id, str) or not segment_id or segment_id in seen:
            raise ValueError("invalid segment identity")
        if not isinstance(text, str) or not text or len(text.encode("utf-8")) > MAX_TEXT_BYTES:
            raise ValueError("invalid segment text")
        seen.add(segment_id)
    return segments


def run(model_dir):
    try:
        translator, tokenizer = load_runtime(model_dir)
    except Exception:
        traceback.print_exc(file=sys.stderr)
        emit({
            "type": "startup_error",
            "protocol": PROTOCOL_VERSION,
            "engine": ENGINE_ID,
            "code": "runtime_unavailable",
            "message": "The local translation runtime or model is unavailable. Run listen setup --translate en:pt-BR again.",
        })
        return 1

    emit({"type": "ready", "protocol": PROTOCOL_VERSION, "engine": ENGINE_ID})
    for raw in sys.stdin.buffer:
        if len(raw) > MAX_LINE_BYTES:
            emit({"type": "error", "code": "request_too_large", "message": "The translation request is too large."})
            continue
        try:
            request = json.loads(raw)
        except Exception:
            emit({"type": "error", "code": "invalid_request", "message": "The translation request could not be read."})
            continue
        if isinstance(request, dict) and request.get("type") == "shutdown":
            return 0
        request_id = request.get("request_id", "") if isinstance(request, dict) else ""
        try:
            segments = validate_request(request)
            translated = translate_batch(translator, tokenizer, segments)
            emit({"type": "translated", "request_id": request_id, "segments": translated})
        except Exception:
            traceback.print_exc(file=sys.stderr)
            emit({
                "type": "error",
                "request_id": request_id,
                "code": "translation_failed",
                "message": "The local model could not translate this passage. Completed cache entries remain available; run listen again to resume.",
            })
    return 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-dir", required=True)
    args = parser.parse_args()
    return run(__import__("pathlib").Path(args.model_dir))


if __name__ == "__main__":
    raise SystemExit(main())
