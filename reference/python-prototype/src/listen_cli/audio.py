"""WAV cache handling and final audio encoding."""

from __future__ import annotations

import os
import shutil
import subprocess
import wave
from pathlib import Path
from typing import Iterable

import numpy as np


SAMPLE_RATE = 24_000
CHANNELS = 1
SAMPLE_WIDTH = 2
SEGMENT_PAUSE_SECONDS = 0.18
CHUNK_PAUSE_SECONDS = 0.32


def is_complete_wav(path: Path) -> bool:
    try:
        with wave.open(str(path), "rb") as audio:
            return (
                audio.getnchannels() == CHANNELS
                and audio.getsampwidth() == SAMPLE_WIDTH
                and audio.getframerate() == SAMPLE_RATE
                and audio.getnframes() > 0
            )
    except (OSError, EOFError, wave.Error):
        return False


def write_kokoro_chunk(pipeline: object, text: str, voice: str, speed: float, destination: Path) -> None:
    """Generate one cacheable WAV chunk with Kokoro's pipeline."""
    temporary = destination.with_suffix(".partial")
    silence = b"\x00" * round(SAMPLE_RATE * SEGMENT_PAUSE_SECONDS) * SAMPLE_WIDTH
    generated = 0

    try:
        with wave.open(str(temporary), "wb") as wav_file:
            wav_file.setnchannels(CHANNELS)
            wav_file.setsampwidth(SAMPLE_WIDTH)
            wav_file.setframerate(SAMPLE_RATE)

            for _, _, audio in pipeline(text, voice=voice, speed=speed, split_pattern=r"\n+"):
                if audio is None:
                    continue
                samples = audio.detach().cpu().numpy() if hasattr(audio, "detach") else np.asarray(audio)
                samples = np.asarray(samples, dtype=np.float32).reshape(-1)
                if samples.size == 0:
                    continue
                samples = np.nan_to_num(samples, nan=0.0, posinf=1.0, neginf=-1.0)
                pcm = (np.clip(samples, -1.0, 1.0) * 32767.0).astype("<i2", copy=False)
                if generated:
                    wav_file.writeframesraw(silence)
                wav_file.writeframesraw(pcm.tobytes())
                generated += 1

        if generated == 0:
            raise RuntimeError("Kokoro returned no audio for a text chunk.")
        os.replace(temporary, destination)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def _write_joined_wav(chunks: Iterable[Path], destination: Path) -> None:
    chunk_paths = list(chunks)
    if not chunk_paths:
        raise ValueError("There is no audio to join.")

    silence_frames = round(SAMPLE_RATE * CHUNK_PAUSE_SECONDS)
    silence = b"\x00" * silence_frames * CHANNELS * SAMPLE_WIDTH

    with wave.open(str(destination), "wb") as output:
        output.setnchannels(CHANNELS)
        output.setsampwidth(SAMPLE_WIDTH)
        output.setframerate(SAMPLE_RATE)
        for index, path in enumerate(chunk_paths):
            if not is_complete_wav(path):
                raise ValueError(f"Cached audio chunk is incomplete: {path}")
            with wave.open(str(path), "rb") as part:
                while frames := part.readframes(65_536):
                    output.writeframesraw(frames)
            if index + 1 < len(chunk_paths):
                output.writeframesraw(silence)


def encode_output(chunks: Iterable[Path], output: Path) -> None:
    """Join cached WAVs and atomically write a WAV or MP3 output file."""
    output = output.expanduser()
    suffix = output.suffix.lower()
    if suffix not in {".mp3", ".wav"}:
        raise ValueError("Output must end in .mp3 or .wav.")
    if suffix == ".mp3" and shutil.which("ffmpeg") is None:
        raise RuntimeError("ffmpeg is required to create MP3 files. Install it with: sudo pacman -S ffmpeg")

    output.parent.mkdir(parents=True, exist_ok=True)
    temporary_wav = output.with_name(f".{output.stem}.{os.getpid()}.partial.wav")
    temporary_output = output.with_name(f".{output.stem}.{os.getpid()}.partial{suffix}")
    temporary_wav.unlink(missing_ok=True)
    temporary_output.unlink(missing_ok=True)

    try:
        _write_joined_wav(chunks, temporary_wav)
        if suffix == ".wav":
            os.replace(temporary_wav, output)
            return

        subprocess.run(
            [
                "ffmpeg",
                "-hide_banner",
                "-loglevel",
                "error",
                "-y",
                "-i",
                str(temporary_wav),
                "-vn",
                "-codec:a",
                "libmp3lame",
                "-q:a",
                "2",
                str(temporary_output),
            ],
            check=True,
        )
        os.replace(temporary_output, output)
    except subprocess.CalledProcessError as error:
        raise RuntimeError("ffmpeg could not encode the final MP3.") from error
    finally:
        temporary_wav.unlink(missing_ok=True)
        temporary_output.unlink(missing_ok=True)
