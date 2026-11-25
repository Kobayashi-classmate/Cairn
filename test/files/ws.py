# SPDX-License-Identifier: AGPL-3.0-only
"""A minimal WebSocket client (standard library only) for the terminal test.

    python3 -I ws.py URL COOKIEJAR INPUT EXPECT [--exit]

Connects to a terminal session the way a browser does (the cookies of
COOKIEJAR, and whatever one-time ticket URL carries), sends INPUT (with "\\n" for newlines) as one
binary frame, and reads until the output contains EXPECT. With --exit it
then waits for the server's {"type":"exit"}. Prints the output; exits 1 on
a timeout.
"""
import base64
import os
import socket
import ssl
import sys
import time
from urllib.parse import urlparse


def cookie_header(jar):
    pairs = []
    with open(jar) as f:
        for line in f:
            if line.startswith("#HttpOnly_"):
                line = line[len("#HttpOnly_"):]
            elif line.startswith("#") or not line.strip():
                continue
            fields = line.rstrip("\n").split("\t")
            if len(fields) >= 7:
                pairs.append(fields[5] + "=" + fields[6])
    return "; ".join(pairs)


def frame(opcode, payload):
    head = bytes([0x80 | opcode])
    n = len(payload)
    if n < 126:
        head += bytes([0x80 | n])
    elif n < 65536:
        head += bytes([0x80 | 126]) + n.to_bytes(2, "big")
    else:
        head += bytes([0x80 | 127]) + n.to_bytes(8, "big")
    mask = os.urandom(4)
    return head + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload))


def read_exact(sock, n):
    data = b""
    while len(data) < n:
        chunk = sock.recv(n - len(data))
        if not chunk:
            raise EOFError
        data += chunk
    return data


def read_frame(sock):
    b1, b2 = read_exact(sock, 2)
    n = b2 & 0x7F
    if n == 126:
        n = int.from_bytes(read_exact(sock, 2), "big")
    elif n == 127:
        n = int.from_bytes(read_exact(sock, 8), "big")
    return b1 & 0x0F, read_exact(sock, n)


def main():
    url, jar, text, expect = sys.argv[1:5]
    wait_exit = "--exit" in sys.argv[5:]
    u = urlparse(url)
    ctx = ssl.create_default_context()
    ctx.minimum_version = ssl.TLSVersion.TLSv1_2
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    raw = socket.create_connection((u.hostname, u.port), timeout=20)
    sock = ctx.wrap_socket(raw, server_hostname=u.hostname)
    key = base64.b64encode(os.urandom(16)).decode()
    target = u.path + ("?" + u.query if u.query else "")
    req = (f"GET {target} HTTP/1.1\r\nHost: {u.netloc}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
           f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nCookie: {cookie_header(jar)}\r\n\r\n")
    sock.sendall(req.encode())
    head = b""
    while b"\r\n\r\n" not in head:
        head += read_exact(sock, 1)
    if b" 101 " not in head.split(b"\r\n")[0]:
        print(head.decode(errors="replace"))
        sys.exit(1)
    sock.sendall(frame(2, text.replace("\\n", "\n").encode()))
    out, deadline, exited = "", time.time() + 20, False
    while time.time() < deadline:
        try:
            op, data = read_frame(sock)
        except (EOFError, socket.timeout):
            break
        if op == 2:
            out += data.decode(errors="replace")
        elif op == 1:
            sys.stderr.write("text frame: %s\n" % data.decode(errors="replace"))
            if b'"exit"' in data:
                exited = True
                break
        elif op == 8:
            break
        if expect in out and not wait_exit:
            break
    print(out)
    if expect not in out or (wait_exit and not exited):
        sys.exit(1)


main()
