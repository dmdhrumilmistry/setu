#!/usr/bin/env python3
"""Run two spike peers against each other: relay the offerer's SDP line to
the answerer and back, wait for the receiving side to finish, and print both
peers' logs. Usage: pair.py "<offerer cmd>" "<answerer cmd>" [timeout-seconds]
"""
import shlex, subprocess, sys, threading

offer_cmd, answer_cmd = sys.argv[1], sys.argv[2]
timeout = float(sys.argv[3]) if len(sys.argv) > 3 else 60
a = subprocess.Popen(shlex.split(offer_cmd), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
b = subprocess.Popen(shlex.split(answer_cmd), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
logs = {"offerer": [], "answerer": []}
def drain(name, p):
    for line in p.stderr:
        logs[name].append(line.rstrip())
for name, p in (("offerer", a), ("answerer", b)):
    threading.Thread(target=drain, args=(name, p), daemon=True).start()
b.stdin.write(a.stdout.readline()); b.stdin.flush()
a.stdin.write(b.stdout.readline()); a.stdin.flush()
receivers = [p for p, cmd in ((a, offer_cmd), (b, answer_cmd)) if "--recv" in cmd or "-recv" in cmd]
ok = True
for p in receivers:
    try:
        p.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        ok = False
import os
cpu = {}
for name, p in (("offerer", a), ("answerer", b)):
    if p.poll() is None:
        p.kill()
    _, status, ru = os.wait4(p.pid, 0) if p.returncode is None else (0, 0, None)
    if ru is not None:
        cpu[name] = ru.ru_utime + ru.ru_stime
for name in logs:
    for line in logs[name]:
        print(f"{name}: {line}")
for name, c in cpu.items():
    print(f"{name}: CPU {c:.2f}s")
print("OK" if ok else "TIMEOUT")
