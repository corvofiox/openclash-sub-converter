#!/usr/bin/env python3
"""定位并清理 25500 占用者"""
import os, signal

target = None
for fname in ('/proc/net/tcp', '/proc/net/tcp6'):
    try:
        with open(fname) as f:
            for line in f.readlines()[1:]:
                parts = line.split()
                local = parts[1]
                st = parts[3]
                port = local.rsplit(':', 1)[1]
                if int(port, 16) == 25500:
                    target = parts[9]
                    print(f"{fname}: state={st} inode={target}")
    except FileNotFoundError:
        pass

if target:
    for pid in os.listdir('/proc'):
        if not pid.isdigit():
            continue
        try:
            fds = os.listdir(f'/proc/{pid}/fd')
        except OSError:
            continue
        for fd in fds:
            try:
                link = os.readlink(f'/proc/{pid}/fd/{fd}')
            except OSError:
                continue
            if f'socket:[{target}]' in link:
                try:
                    cmdline = open(f'/proc/{pid}/cmdline').read().replace('\0', ' ')
                except OSError:
                    cmdline = '?'
                print(f"kill PID {pid}: {cmdline[:100]}")
                os.kill(int(pid), signal.SIGKILL)
else:
    print("25500 无监听")
