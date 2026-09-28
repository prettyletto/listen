"""Long-lived JSONL inference worker for the Go listen command."""
from __future__ import annotations

import argparse
import contextlib
import json
import math
import os
from pathlib import Path
import sys
import warnings
import wave

PROTOCOL = 1
SAMPLE_RATE = 24_000
SAMPLE_WIDTH = 2
SEGMENT_PAUSE_SECONDS = 0.18
SUPPORTED_LANGUAGES = set("abefhip")


def emit(value: dict) -> None:
    sys.stdout.write(json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def load_pipeline(language: str):
    if language not in SUPPORTED_LANGUAGES:
        raise ValueError("The selected voice language is not supported by the local speech engine.")
    # The engine and PyTorch may print progress information while importing or
    # loading. The worker's stdout is reserved exclusively for JSONL messages.
    with contextlib.redirect_stdout(sys.stderr), warnings.catch_warnings():
        warnings.filterwarnings("ignore", message="dropout option adds dropout after all but last recurrent layer.*")
        warnings.filterwarnings("ignore", category=FutureWarning, module=r"torch\.nn\.utils\.weight_norm")
        warnings.filterwarnings("ignore", category=FutureWarning, module=r"torch\.jit\._script")
        from kokoro import KPipeline

        return KPipeline(lang_code=language, repo_id="hexgrad/Kokoro-82M", device="cpu")


def output_path(request: dict, cache_root: Path) -> Path:
    raw = request.get("output")
    if not isinstance(raw, str) or not raw:
        raise ValueError("The Go client did not provide an audio output path.")
    destination = Path(raw)
    if not destination.is_absolute():
        raise ValueError("The audio output path must be absolute.")
    root = cache_root.resolve()
    resolved = destination.resolve()
    if resolved == root or not resolved.is_relative_to(root):
        raise ValueError("The audio output path is outside the approved cache directory.")
    destination.parent.mkdir(parents=True, exist_ok=True)
    # Re-resolve after directory creation to reject paths traversing a symlink.
    resolved = destination.resolve()
    if resolved == root or not resolved.is_relative_to(root):
        raise ValueError("The audio output path is outside the approved cache directory.")
    return destination


def synthesize(pipeline, request: dict, cache_root: Path) -> dict:
    request_id = request.get("request_id")
    segment_id = request.get("segment_id")
    seq = request.get("seq")
    text = request.get("text")
    voice = request.get("voice")
    speed = request.get("speed")
    if not isinstance(request_id, str) or not request_id:
        raise ValueError("The request has no ID.")
    if not isinstance(segment_id, str) or not segment_id:
        raise ValueError("The request has no segment ID.")
    if not isinstance(seq, int) or seq < 0:
        raise ValueError("The request has an invalid sequence number.")
    if not isinstance(text, str) or not text.strip():
        raise ValueError("The request has no spoken text.")
    if not isinstance(voice, str) or len(voice) < 2 or voice[0].lower() not in SUPPORTED_LANGUAGES:
        raise ValueError("The request has an unsupported voice ID.")
    if not isinstance(speed, (float, int)) or not math.isfinite(speed) or not 0.5 <= speed <= 2.0:
        raise ValueError("Speech speed must be between 0.5 and 2.0.")

    destination = output_path(request, cache_root)
    temporary = Path(str(destination) + ".partial")
    generated = 0
    silence = b"\x00" * round(SAMPLE_RATE * SEGMENT_PAUSE_SECONDS) * SAMPLE_WIDTH
    try:
        with wave.open(str(temporary), "wb") as wav_file:
            wav_file.setnchannels(1)
            wav_file.setsampwidth(SAMPLE_WIDTH)
            wav_file.setframerate(SAMPLE_RATE)
            # Kokoro yields sentence-sized audio pieces. Keep small pauses when
            # joining them into one stable, cacheable semantic segment.
            # Some model versions write progress to stdout during inference;
            # keep the JSONL protocol clean for the Go client.
            with contextlib.redirect_stdout(sys.stderr):
                for _, _, audio in pipeline(text, voice=voice, speed=float(speed), split_pattern=r"\n+"):
                    if audio is None:
                        continue
                    if hasattr(audio, "detach"):
                        audio = audio.detach().cpu().numpy()
                    import numpy as np

                    samples = np.asarray(audio, dtype=np.float32).reshape(-1)
                    if samples.size == 0:
                        continue
                    samples = np.nan_to_num(samples, nan=0.0, posinf=1.0, neginf=-1.0)
                    pcm = (np.clip(samples, -1.0, 1.0) * 32767.0).astype("<i2", copy=False)
                    if generated:
                        wav_file.writeframesraw(silence)
                    wav_file.writeframesraw(pcm.tobytes())
                    generated += 1
        if generated == 0:
            raise RuntimeError("The speech engine returned no audio for this segment.")
        with temporary.open("r+b") as audio_file:
            os.fsync(audio_file.fileno())
        with wave.open(str(temporary), "rb") as check:
            if check.getnchannels() != 1 or check.getsampwidth() != SAMPLE_WIDTH or check.getframerate() != SAMPLE_RATE or check.getnframes() <= 0:
                raise RuntimeError("The speech engine produced an invalid WAV file.")
            duration_ms = round(check.getnframes() * 1000 / SAMPLE_RATE)
        os.replace(temporary, destination)
        directory_fd = os.open(destination.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
        return {
            "type": "synthesized",
            "request_id": request_id,
            "segment_id": segment_id,
            "seq": seq,
            "path": str(destination),
            "duration_ms": duration_ms,
        }
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def main() -> int:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--cache-root", required=True)
    parser.add_argument("--voice", default="af_heart")
    args = parser.parse_args()
    cache_root = Path(args.cache_root).expanduser().resolve()
    cache_root.mkdir(parents=True, exist_ok=True)
    pipelines = {}
    try:
        language = args.voice[0].lower()
        pipelines[language] = load_pipeline(language)
    except Exception as exc:
        emit({"type": "startup_error", "protocol": PROTOCOL, "code": "kokoro_unavailable", "message": str(exc)})
        return 2

    emit({"type": "ready", "protocol": PROTOCOL, "engine": "kokoro", "sample_rate": SAMPLE_RATE})
    for raw in sys.stdin:
        request = None
        try:
            request = json.loads(raw)
            if request.get("type") == "shutdown":
                return 0
            if request.get("type") != "synthesize":
                raise ValueError("Unsupported worker request type.")
            voice = request.get("voice", args.voice)
            language = voice[0].lower() if isinstance(voice, str) and voice else ""
            if language not in pipelines:
                pipelines[language] = load_pipeline(language)
            emit(synthesize(pipelines[language], request, cache_root))
        except Exception as exc:
            emit({
                "type": "error",
                "request_id": request.get("request_id") if isinstance(request, dict) else None,
                "code": "synthesis_failed",
                "message": str(exc),
            })
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
