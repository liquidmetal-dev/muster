"""Connect to a guest vsock port through a Firecracker vsock socket.

Usage: vsock-probe.py <uds path> <port>. Exits 0 when the guest accepts the connection.
"""

import socket
import sys

path, port = sys.argv[1], sys.argv[2]
sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.settimeout(2)
try:
    sock.connect(path)
    sock.sendall(f"CONNECT {port}\n".encode())
    reply = sock.recv(64)
except OSError:
    sys.exit(1)
finally:
    sock.close()
sys.exit(0 if reply.startswith(b"OK ") else 1)
