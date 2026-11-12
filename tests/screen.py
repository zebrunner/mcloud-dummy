#!/usr/bin/env python3
"""Reads the screen of a device from the screen websocket of the STF provider (what the STF UI shows).

usage: screen.py <host> <screen port> [seconds] [out.jpg] [size]

size is the size the UI asks for (360x640 by default, e.g. the box of the screen in the browser); minicap keeps the
aspect ratio of the device within it. Prints the size of the last JPEG frame.
"""

import base64
import os
import socket
import struct
import sys
import time

host, port = sys.argv[1], int(sys.argv[2])
seconds = float(sys.argv[3]) if len(sys.argv) > 3 else 5
out = sys.argv[4] if len(sys.argv) > 4 else ""
size = sys.argv[5] if len(sys.argv) > 5 else "360x640"

s = socket.create_connection((host, port), timeout=10)
key = base64.b64encode(os.urandom(16)).decode()
s.sendall(
    (
        f"GET / HTTP/1.1\r\nHost: {host}:{port}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
        f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n"
    ).encode()
)
buf = b""
while b"\r\n\r\n" not in buf:
    buf += s.recv(4096)
head, buf = buf.split(b"\r\n\r\n", 1)
assert b" 101 " in head.split(b"\r\n")[0], head


def send_text(text):
    payload = text.encode()
    mask = os.urandom(4)
    masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    s.sendall(bytes([0x81, 0x80 | len(payload)]) + mask + masked)


def read_exact(n):
    global buf
    while len(buf) < n:
        chunk = s.recv(65536)
        if not chunk:
            raise EOFError("closed")
        buf += chunk
    data, buf = buf[:n], buf[n:]
    return data


def read_frame():
    b0, b1 = read_exact(2)
    n = b1 & 0x7F
    if n == 126:
        n = struct.unpack(">H", read_exact(2))[0]
    elif n == 127:
        n = struct.unpack(">Q", read_exact(8))[0]
    return b0 & 0x0F, read_exact(n)


def jpeg_size(data):
    """The width and height of a JPEG from its SOF marker."""
    i = 2
    while i + 9 < len(data):
        if data[i] != 0xFF:
            return None
        marker, length = data[i + 1], struct.unpack(">H", data[i + 2 : i + 4])[0]
        if marker in (0xC0, 0xC1, 0xC2):
            h, w = struct.unpack(">HH", data[i + 5 : i + 9])
            return w, h
        i += 2 + length
    return None


send_text(f"size {size}")
send_text("on")
frames, start, first, last = 0, time.time(), None, None
s.settimeout(2)
while time.time() - start < seconds:
    try:
        op, data = read_frame()
    except TimeoutError:
        continue
    if op == 1:
        text = data.decode(errors="replace")
        if text != "ping":
            print("text:", text[:200])
    elif op == 2:
        frames += 1
        last = data
        if first is None:
            first = time.time() - start
print(
    f"frames={frames} first_frame_after={first if first is None else round(first, 2)}s "
    f"jpeg={last[:3].hex() if last else None} size={len(last) if last else 0} "
    f"frame={'x'.join(map(str, jpeg_size(last) or ())) if last else None}"
)
if out and last:
    with open(out, "wb") as f:
        f.write(last)
sys.exit(0 if frames > 0 and last[:3] == b"\xff\xd8\xff" else 1)
