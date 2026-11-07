#!/usr/bin/env python3
# -*- coding: utf-8 -*-

"""
223CRASH — PYTHON FULL ENVIRONMENT AUDIT
Version: 1.0.0
Python: 3.13+
Platform: Termux / Android ARM64

Complete read-only diagnostic.
No modification of 223CRASH source code.
"""

from __future__ import annotations

import asyncio
import bz2
import ctypes
import datetime
import hashlib
import importlib.util
import json
import lzma
import multiprocessing
import os
import platform
import shutil
import site
import socket
import sqlite3
import ssl
import subprocess
import sys
import sysconfig
import tempfile
import threading
import traceback
import venv
import zlib

from pathlib import Path
from typing import Any


class AuditResult:

    def __init__(self, name: str):
        self.name = name
        self.success = False
        self.details: Any = None
        self.error: str | None = None


class PythonFullAudit:

    VERSION = "1.0.0"

    def __init__(self):
        self.results: list[AuditResult] = []

    def execute(self, name: str, callback):
        result = AuditResult(name)

        try:
            result.details = callback()
            result.success = True
        except Exception as exc:
            result.error = f"{type(exc).__name__}: {exc}"

        self.results.append(result)

    @staticmethod
    def python_information():
        return {
            "version": sys.version,
            "executable": sys.executable,
            "prefix": sys.prefix,
            "base_prefix": sys.base_prefix,
            "implementation": platform.python_implementation(),
            "architecture": platform.machine(),
            "pointer_bits": ctypes.sizeof(ctypes.c_void_p) * 8,
            "byte_order": sys.byteorder,
        }

    @staticmethod
    def ssl_information():
        return {
            "openssl": ssl.OPENSSL_VERSION,
            "verify_paths": str(ssl.get_default_verify_paths()),
        }

    @staticmethod
    def sqlite_information():
        return {
            "version": sqlite3.sqlite_version,
            "threadsafety": sqlite3.threadsafety,
        }

    @staticmethod
    def pip_information():
        return subprocess.check_output(
            [sys.executable, "-m", "pip", "--version"],
            text=True,
            stderr=subprocess.STDOUT,
        ).strip()

    @staticmethod
    def standard_library():
        modules = [
            "asyncio", "argparse", "bz2", "collections",
            "concurrent.futures", "configparser", "contextlib",
            "csv", "ctypes", "dataclasses", "decimal",
            "email", "fractions", "functools", "hashlib",
            "hmac", "http", "importlib", "inspect", "itertools",
            "json", "logging", "lzma", "multiprocessing",
            "pathlib", "pickle", "queue", "random", "re",
            "secrets", "shutil", "signal", "socket", "sqlite3",
            "ssl", "statistics", "string", "subprocess",
            "tempfile", "threading", "time", "traceback",
            "typing", "unittest", "urllib", "uuid", "venv",
            "xml", "zlib",
        ]

        missing = [
            name for name in modules
            if importlib.util.find_spec(name) is None
        ]

        return {
            "total": len(modules),
            "available": len(modules) - len(missing),
            "missing": missing,
        }

    @staticmethod
    def build_tools():
        return {
            name: shutil.which(name)
            for name in ("clang", "make", "cmake", "pkg-config")
        }

    @staticmethod
    def filesystem_information():
        return {
            "working_directory": os.getcwd(),
            "temporary_directory": tempfile.gettempdir(),
            "stdlib": sysconfig.get_path("stdlib"),
            "purelib": sysconfig.get_path("purelib"),
            "platlib": sysconfig.get_path("platlib"),
            "site_packages": site.getsitepackages(),
            "user_site": site.getusersitepackages(),
        }

    @staticmethod
    def environment_information():
        return {
            "platform": platform.platform(),
            "system": platform.system(),
            "release": platform.release(),
            "machine": platform.machine(),
            "processor": platform.processor(),
            "python_version": platform.python_version(),
            "cpu_count": os.cpu_count(),
        }

    @staticmethod
    def compression_test():
        payload = b"223CRASH-PYTHON-FULL-AUDIT"
        compressed = zlib.compress(payload)

        if zlib.decompress(compressed) != payload:
            raise RuntimeError("Zlib verification failed")

        return {
            "original_size": len(payload),
            "compressed_size": len(compressed),
            "verification": "OK",
        }

    @staticmethod
    def hashing_test():
        payload = b"223CRASH"

        return {
            "sha256": hashlib.sha256(payload).hexdigest(),
            "sha512": hashlib.sha512(payload).hexdigest(),
        }

    @staticmethod
    def sqlite_test():
        connection = sqlite3.connect(":memory:")

        try:
            connection.execute(
                "CREATE TABLE audit (id INTEGER PRIMARY KEY, status TEXT)"
            )
            connection.execute(
                "INSERT INTO audit(status) VALUES (?)",
                ("OK",),
            )

            result = connection.execute(
                "SELECT status FROM audit WHERE id = 1"
            ).fetchone()

            if result is None or result[0] != "OK":
                raise RuntimeError("SQLite verification failed")

            return {
                "database": "in-memory",
                "verification": "OK",
            }

        finally:
            connection.close()

    @staticmethod
    def asyncio_test():
        async def worker():
            await asyncio.sleep(0)
            return "OK"

        return asyncio.run(worker())

    @staticmethod
    def threading_test():
        result = []

        thread = threading.Thread(
            target=lambda: result.append("OK")
        )
        thread.start()
        thread.join()

        if result != ["OK"]:
            raise RuntimeError("Threading verification failed")

        return "OK"

    @staticmethod
    def multiprocessing_information():
        return {
            "start_method": multiprocessing.get_start_method(),
            "available_methods": multiprocessing.get_all_start_methods(),
        }

    @staticmethod
    def venv_information():
        return {
            "available": venv.EnvBuilder is not None,
            "module": venv.__file__,
        }

    @staticmethod
    def socket_information():
        return {
            "hostname": socket.gethostname(),
            "socket_available": True,
        }

    @staticmethod
    def datetime_information():
        return {
            "utc": datetime.datetime.now(
                datetime.timezone.utc
            ).isoformat(),
        }

    def run(self):
        print()
        print("=" * 68)
        print("223CRASH — PYTHON FULL ENVIRONMENT AUDIT")
        print("=" * 68)

        checks = [
            ("PYTHON", self.python_information),
            ("OPENSSL", self.ssl_information),
            ("SQLITE", self.sqlite_information),
            ("PIP", self.pip_information),
            ("STANDARD LIBRARY", self.standard_library),
            ("BUILD TOOLS", self.build_tools),
            ("FILESYSTEM", self.filesystem_information),
            ("ENVIRONMENT", self.environment_information),
            ("COMPRESSION", self.compression_test),
            ("HASHING", self.hashing_test),
            ("SQLITE FUNCTIONAL TEST", self.sqlite_test),
            ("ASYNCIO", self.asyncio_test),
            ("THREADING", self.threading_test),
            ("MULTIPROCESSING", self.multiprocessing_information),
            ("VIRTUAL ENVIRONMENT", self.venv_information),
            ("SOCKET", self.socket_information),
            ("DATETIME", self.datetime_information),
        ]

        for name, callback in checks:
            self.execute(name, callback)

        for result in self.results:
            status = "OK" if result.success else "ERROR"
            print(f"\n[{status}] {result.name}")

            if result.success:
                print(json.dumps(
                    result.details,
                    indent=2,
                    ensure_ascii=False,
                    default=str,
                ))
            else:
                print(result.error)

        passed = sum(
            1 for result in self.results
            if result.success
        )

        failed = len(self.results) - passed

        print("\n" + "=" * 68)
        print(f"TOTAL CHECKS : {len(self.results)}")
        print(f"PASSED       : {passed}")
        print(f"FAILED       : {failed}")
        print("=" * 68)

        return 0 if failed == 0 else 1


def main():
    return PythonFullAudit().run()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("\nAudit interrupted.")
        sys.exit(130)
    except Exception:
        traceback.print_exc()
        sys.exit(1)
