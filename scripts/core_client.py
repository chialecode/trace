#!/usr/bin/env python3
"""A small client for a TRACE daemon: framed named-pipe control calls and stdio adapter frames.

Shared by the package check and the real-site driver so both speak to the same product
interface the desktop uses. A caller never reads a Cookie back: credentials go in through
`credential.import` and only metadata comes out.
"""
from __future__ import annotations

import json
import os
import queue
import struct
import subprocess
import threading
import time
from pathlib import Path

PROTOCOL = "0.1"
MAX_FRAME = 1024 * 1024
NO_WINDOW = getattr(subprocess, "CREATE_NO_WINDOW", 0)


def read_pipe_frame(stream, timeout: float = 30):
    """Read one length-prefixed JSON frame from a synchronous Windows named pipe.

    A synchronous pipe handle cannot have a permanently blocking background read while the same
    handle writes a request, so poll the available bytes with the OS API and read only that many,
    under a fixed deadline.
    """
    import ctypes
    import msvcrt
    from ctypes import wintypes as W

    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.PeekNamedPipe.argtypes = [W.HANDLE, ctypes.c_void_p, W.DWORD, ctypes.c_void_p, ctypes.POINTER(W.DWORD), ctypes.c_void_p]
    handle = msvcrt.get_osfhandle(stream.fileno())
    deadline = time.monotonic() + timeout

    def exact(size: int) -> bytes:
        data = b""
        while len(data) < size:
            available = W.DWORD()
            if not kernel.PeekNamedPipe(handle, None, 0, None, ctypes.byref(available), None):
                raise OSError(ctypes.get_last_error(), "control pipe disconnected")
            if available.value:
                block = stream.read(min(size - len(data), available.value))
                if not block:
                    raise EOFError("control pipe ended")
                data += block
            elif time.monotonic() >= deadline:
                raise TimeoutError("control pipe response timeout")
            else:
                time.sleep(0.01)
        return data

    size = struct.unpack("<I", exact(4))[0]
    if size > MAX_FRAME:
        raise ValueError("oversize frame")
    return json.loads(exact(size))


def write_frame(stream, value) -> None:
    data = json.dumps(value, ensure_ascii=False).encode("utf8")
    stream.write(struct.pack("<I", len(data)) + data)
    stream.flush()


class Reader:
    """Background reader for a child process stream: JSON lines or length-prefixed frames."""

    def __init__(self, stream, lines: bool = False):
        self.queue: queue.Queue = queue.Queue()

        def exact(n: int) -> bytes:
            out = b""
            while len(out) < n:
                chunk = stream.read(n - len(out))
                if not chunk:
                    raise EOFError("truncated frame")
                out += chunk
            return out

        def pump():
            try:
                while True:
                    if lines:
                        raw = stream.readline()
                        if not raw:
                            raise EOFError("process exited")
                        value = json.loads(raw)
                    else:
                        size = struct.unpack("<I", exact(4))[0]
                        if size > MAX_FRAME:
                            raise ValueError("oversize frame")
                        value = json.loads(exact(size))
                    self.queue.put(value)
            except Exception as error:  # the consumer sees the failure instead of a hang
                self.queue.put(error)

        threading.Thread(target=pump, daemon=True).start()

    def get(self, timeout: float = 30):
        value = self.queue.get(timeout=timeout)
        if isinstance(value, Exception):
            raise value
        return value


class CoreError(RuntimeError):
    def __init__(self, command: str, error):
        super().__init__(f"{command}: {error}")
        self.command = command
        self.error = error


class Core:
    """One daemon process on an isolated Profile, controlled over its named pipe."""

    def __init__(self, daemon: Path, profile: Path, env: dict | None = None, client: str = "verifier"):
        self.daemon = Path(daemon)
        self.profile = Path(profile)
        self.client = client
        self.env = dict(os.environ if env is None else env)
        self.env.pop("TRACE_PROFILE", None)
        self.proc: subprocess.Popen | None = None
        self.pipe = None
        self.ready: dict = {}

    def __enter__(self) -> "Core":
        self.proc = subprocess.Popen(
            [str(self.daemon), "start", "--profile", str(self.profile)],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=self.env, creationflags=NO_WINDOW,
        )
        self.ready = Reader(self.proc.stdout, lines=True).get(60)
        if self.ready.get("status") != "ready":
            raise RuntimeError(f"daemon did not become ready: {self.ready}")
        for _ in range(60):
            try:
                self.pipe = open(self.ready["pipe"], "r+b", buffering=0)
                break
            except OSError:
                time.sleep(0.1)
        if self.pipe is None:
            raise RuntimeError("control pipe unavailable")
        profile = json.loads(self.profile.read_text(encoding="utf8"))
        write_frame(self.pipe, {"client": self.client, "protocol_version": PROTOCOL, "archive_id": profile["archive_id"]})
        hello = read_pipe_frame(self.pipe)
        if hello.get("frame") != "hello":
            raise RuntimeError(f"unexpected greeting: {hello}")
        return self

    def call(self, command: str, args: dict | None = None, timeout: float = 120, expect_ok: bool = True):
        rid = str(time.time_ns())
        write_frame(self.pipe, {"protocol_version": PROTOCOL, "request_id": rid, "command": command, "idempotency_key": rid, "args": args or {}})
        deadline = time.monotonic() + timeout
        while True:
            frame = read_pipe_frame(self.pipe, max(1.0, deadline - time.monotonic()))
            if frame.get("request_id") == rid:
                if expect_ok and not frame.get("ok"):
                    raise CoreError(command, frame.get("error"))
                return frame.get("result") if frame.get("ok") else frame

    def __exit__(self, *exc) -> None:
        try:
            if self.proc and self.proc.poll() is None and self.pipe:
                try:
                    self.call("daemon.shutdown", {}, timeout=20, expect_ok=False)
                except Exception:
                    pass
        finally:
            if self.pipe:
                self.pipe.close()
            if self.proc:
                try:
                    self.proc.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    self.proc.kill()
                    self.proc.wait(timeout=10)
                self.proc.stdout.close()


def adapter_hello(exe: Path, env: dict | None = None) -> tuple[dict, subprocess.Popen, Reader]:
    """Start one adapter executable and return its answer to `hello` together with the live process."""
    env = dict(os.environ if env is None else env)
    proc = subprocess.Popen([str(exe)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=env, creationflags=NO_WINDOW)
    reader = Reader(proc.stdout)
    write_frame(proc.stdin, {"protocol_version": PROTOCOL, "request_id": "hello", "method": "hello", "params": {}, "deadline_ms": 5000})
    return reader.get(10), proc, reader
