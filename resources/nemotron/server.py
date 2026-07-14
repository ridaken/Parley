"""Local Nemotron 3.5 ASR sidecar with Parley's /inference contract."""

from __future__ import annotations

import argparse
import io
import json
import logging
import threading
import time
import wave
from email import policy
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import numpy as np
import torch
from transformers import AutoModelForRNNT, AutoProcessor, TextIteratorStreamer


LOG = logging.getLogger("parley-nemotron")
MAX_REQUEST_BYTES = 25 * 1024 * 1024


def read_wav(payload: bytes) -> tuple[np.ndarray, int]:
    with wave.open(io.BytesIO(payload), "rb") as wav_file:
        channels = wav_file.getnchannels()
        sample_width = wav_file.getsampwidth()
        sample_rate = wav_file.getframerate()
        frames = wav_file.readframes(wav_file.getnframes())

    if sample_width != 2:
        raise ValueError(f"expected 16-bit PCM WAV, got {sample_width * 8}-bit audio")
    if channels < 1:
        raise ValueError("WAV contains no audio channels")

    samples = np.frombuffer(frames, dtype="<i2").astype(np.float32)
    if channels > 1:
        samples = samples.reshape(-1, channels).mean(axis=1)
    return samples / 32768.0, sample_rate


def multipart_fields(content_type: str, payload: bytes) -> dict[str, bytes]:
    message = BytesParser(policy=policy.default).parsebytes(
        b"Content-Type: "
        + content_type.encode("ascii")
        + b"\r\nMIME-Version: 1.0\r\n\r\n"
        + payload
    )
    if not message.is_multipart():
        raise ValueError("expected multipart/form-data")
    fields: dict[str, bytes] = {}
    for part in message.iter_parts():
        name = part.get_param("name", header="content-disposition")
        data = part.get_payload(decode=True)
        if name and data is not None:
            fields[name] = data
    return fields


class StreamingSession:
    """One cache-aware RNNT stream, kept separate for each Parley audio source."""

    def __init__(self, transcriber: "Transcriber", stream_id: str, model: AutoModelForRNNT) -> None:
        self.transcriber = transcriber
        self.stream_id = stream_id
        self.model = model
        self.condition = threading.Condition()
        self.audio = np.empty(0, dtype=np.float32)
        self.base_sample = 0
        self.closed = False
        self.started = False
        self.worker: threading.Thread | None = None
        self.output_parts: list[str] = []
        self.error: BaseException | None = None

    def feed(self, samples: np.ndarray) -> str:
        with self.condition:
            if self.closed:
                raise RuntimeError(f"stream {self.stream_id!r} is already closed")
            self.audio = np.concatenate((self.audio, samples))
            self._start_if_ready_locked()
            self.condition.notify_all()
        return self.read_delta(wait_seconds=0.05)

    def finish(self) -> str:
        with self.condition:
            self.closed = True
            if len(self.audio) > 0 and not self.started:
                self.started = True
                self.worker = threading.Thread(target=self._run, daemon=True)
                self.worker.start()
            self.condition.notify_all()
            worker = self.worker
        if worker is not None:
            worker.join(timeout=30)
            if worker.is_alive():
                raise TimeoutError(f"stream {self.stream_id!r} did not finish within 30 seconds")
        return self.read_delta(wait_seconds=0)

    def read_delta(self, wait_seconds: float) -> str:
        deadline = time.monotonic() + wait_seconds
        with self.condition:
            while not self.output_parts and self.error is None and time.monotonic() < deadline:
                self.condition.wait(timeout=max(0, deadline - time.monotonic()))
            if self.error is not None:
                raise RuntimeError(f"stream {self.stream_id!r} failed: {self.error}") from self.error
            text = "".join(self.output_parts)
            self.output_parts.clear()
            return text

    def _start_if_ready_locked(self) -> None:
        if self.started or len(self.audio) < self.transcriber.processor.num_samples_first_audio_chunk:
            return
        self.started = True
        self.worker = threading.Thread(target=self._run, daemon=True)
        self.worker.start()

    def _take_audio(self, start: int, end: int) -> np.ndarray | None:
        with self.condition:
            while self.base_sample + len(self.audio) < end and not self.closed:
                self.condition.wait()
            available_end = self.base_sample + len(self.audio)
            if available_end <= start:
                return None
            offset = start - self.base_sample
            if offset < 0:
                raise RuntimeError("streaming audio cache advanced past a required frame")
            take = min(end, available_end) - start
            chunk = self.audio[offset : offset + take].copy()
            if take < end - start:
                chunk = np.pad(chunk, (0, end - start - take))
            return chunk

    def _trim_before(self, sample_index: int) -> None:
        with self.condition:
            count = min(max(sample_index - self.base_sample, 0), len(self.audio))
            if count:
                self.audio = self.audio[count:]
                self.base_sample += count

    def _feature_generator(self, first_features: torch.Tensor):
        processor = self.transcriber.processor
        yield first_features[:, : processor.num_mel_frames_first_audio_chunk, :]
        mel_frame_index = processor.num_mel_frames_first_audio_chunk
        hop_length = processor.feature_extractor.hop_length
        n_fft = processor.feature_extractor.n_fft
        while True:
            start = mel_frame_index * hop_length - n_fft // 2
            end = start + processor.num_samples_per_audio_chunk
            chunk = self._take_audio(start, end)
            if chunk is None:
                return
            inputs = processor(
                chunk,
                sampling_rate=processor.feature_extractor.sampling_rate,
                is_streaming=True,
                is_first_audio_chunk=False,
                language=self.transcriber.language,
                return_tensors="pt",
            ).to(self.model.device, dtype=self.model.dtype)
            yield inputs.input_features
            mel_frame_index += processor.num_mel_frames_per_audio_chunk
            next_start = mel_frame_index * hop_length - n_fft // 2
            self._trim_before(next_start)

    def _run(self) -> None:
        try:
            processor = self.transcriber.processor
            first_audio = self._take_audio(0, processor.num_samples_first_audio_chunk)
            if first_audio is None:
                return
            first_inputs = processor(
                first_audio,
                sampling_rate=processor.feature_extractor.sampling_rate,
                is_streaming=True,
                is_first_audio_chunk=True,
                language=self.transcriber.language,
                return_tensors="pt",
            ).to(self.model.device, dtype=self.model.dtype)
            streamer = TextIteratorStreamer(processor.tokenizer, skip_special_tokens=True)
            generation_error: list[BaseException] = []

            def generate() -> None:
                try:
                    with torch.inference_mode():
                        generate_kwargs = {
                            **first_inputs,
                            "input_features": self._feature_generator(first_inputs.input_features),
                            "streamer": streamer,
                        }
                        self.model.generate(**generate_kwargs)
                except BaseException as exc:  # surfaced through the next HTTP response
                    generation_error.append(exc)
                finally:
                    streamer.end()

            generation_thread = threading.Thread(target=generate, daemon=True)
            generation_thread.start()
            for text in streamer:
                if text:
                    with self.condition:
                        self.output_parts.append(text)
                        self.condition.notify_all()
            generation_thread.join()
            if generation_error:
                raise generation_error[0]
        except BaseException as exc:
            LOG.exception("stream %s failed", self.stream_id)
            with self.condition:
                self.error = exc
                self.condition.notify_all()


class Transcriber:
    def __init__(self, model_dir: Path, language: str) -> None:
        if not torch.cuda.is_available():
            raise RuntimeError("CUDA is not available to PyTorch")

        LOG.info("loading nvidia/nemotron-3.5-asr-streaming-0.6b from %s", model_dir)
        self.processor = AutoProcessor.from_pretrained(model_dir, local_files_only=True)
        def load_model() -> AutoModelForRNNT:
            model = AutoModelForRNNT.from_pretrained(
                model_dir,
                local_files_only=True,
                dtype=torch.float16,
            ).to("cuda")
            model.eval()
            return model

        # Transformers' RNNT generate() temporarily mutates decoder attributes,
        # so Parley's two normal sources need independent model objects. Sharing
        # one instance across concurrent streams corrupts that temporary state.
        self.models = [load_model(), load_model()]
        self.available_models = list(self.models)
        self.language = language
        self.lock = threading.Lock()
        self.sessions_lock = threading.Lock()
        self.sessions: dict[str, StreamingSession] = {}
        LOG.info(
            "two streaming model slots ready on %s (%s); language=%s",
            torch.cuda.get_device_name(0),
            self.models[0].dtype,
            language,
        )

    def transcribe(self, wav_payload: bytes) -> str:
        audio, sample_rate = read_wav(wav_payload)
        with self.lock, torch.inference_mode():
            inputs = self.processor(
                audio,
                sampling_rate=sample_rate,
                language=self.language,
                return_tensors="pt",
            )
            model = self.models[0]
            inputs = inputs.to(model.device, dtype=model.dtype)
            output = model.generate(**inputs, return_dict_in_generate=True)
            decoded = self.processor.decode(output.sequences, skip_special_tokens=True)
            if isinstance(decoded, list):
                decoded = " ".join(str(item) for item in decoded)
            return str(decoded).strip()

    def stream_feed(self, stream_id: str, wav_payload: bytes) -> str:
        audio, sample_rate = read_wav(wav_payload)
        if sample_rate != self.processor.feature_extractor.sampling_rate:
            raise ValueError(
                f"expected {self.processor.feature_extractor.sampling_rate} Hz audio, got {sample_rate} Hz"
            )
        with self.sessions_lock:
            session = self.sessions.get(stream_id)
            if session is None:
                if not self.available_models:
                    raise RuntimeError("all Nemotron streaming slots are in use")
                session = StreamingSession(self, stream_id, self.available_models.pop())
                self.sessions[stream_id] = session
        return session.feed(audio)

    def stream_finish(self, stream_id: str) -> str:
        with self.sessions_lock:
            session = self.sessions.pop(stream_id, None)
        if session is None:
            return ""
        try:
            return session.finish()
        finally:
            with self.sessions_lock:
                self.available_models.append(session.model)


def handler_for(transcriber: Transcriber):
    class Handler(BaseHTTPRequestHandler):
        server_version = "ParleyNemotron/1"

        def write_json(self, status: int, body: dict[str, object]) -> None:
            payload = json.dumps(body).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
            if self.path != "/":
                self.write_json(404, {"error": "not found"})
                return
            self.write_json(
                200,
                {
                    "status": "ready",
                    "model": "nvidia/nemotron-3.5-asr-streaming-0.6b",
                    "device": "cuda",
                },
            )

        def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
            if self.path not in ("/inference", "/stream"):
                self.write_json(404, {"error": "not found"})
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length <= 0 or length > MAX_REQUEST_BYTES:
                    raise ValueError("invalid request size")
                body = self.rfile.read(length)
                fields = multipart_fields(self.headers.get("Content-Type", ""), body)
                if self.path == "/inference":
                    wav_payload = fields.get("file")
                    if wav_payload is None:
                        raise ValueError("multipart request did not contain a file field")
                    text = transcriber.transcribe(wav_payload)
                else:
                    stream_id = fields.get("stream_id", b"").decode("utf-8")
                    action = fields.get("action", b"").decode("utf-8")
                    if not stream_id:
                        raise ValueError("stream_id is required")
                    if action == "feed":
                        wav_payload = fields.get("file")
                        if wav_payload is None:
                            raise ValueError("stream feed did not contain a file field")
                        text = transcriber.stream_feed(stream_id, wav_payload)
                    elif action == "finish":
                        text = transcriber.stream_finish(stream_id)
                    else:
                        raise ValueError("action must be feed or finish")
                self.write_json(200, {"text": text})
            except ValueError as exc:
                LOG.warning("invalid inference request: %s", exc)
                self.write_json(400, {"error": str(exc)})
            except Exception as exc:  # keep the sidecar alive; Go logs the failed chunk
                LOG.exception("inference failed")
                self.write_json(500, {"error": str(exc)})

        def log_message(self, message: str, *args: object) -> None:
            if self.path == "/stream":
                return
            LOG.info("http: " + message, *args)

    return Handler


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", type=Path, required=True)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8765)
    parser.add_argument("--language", default="en-US")
    args = parser.parse_args()

    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    transcriber = Transcriber(args.model.resolve(), args.language)
    server = ThreadingHTTPServer((args.host, args.port), handler_for(transcriber))
    LOG.info("listening on http://%s:%d", args.host, args.port)
    server.serve_forever()


if __name__ == "__main__":
    main()
