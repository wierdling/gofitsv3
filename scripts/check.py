"""Closing checks for GoFitsV3: gofmt, build, vet and tests scoped to changed packages.

Usage:
  python -I scripts/check.py            # changed .go files vs HEAD (+ untracked)
  python -I scripts/check.py --all      # vet + test every package
  python -I scripts/check.py --no-test  # gofmt + build + vet only
  python -I scripts/check.py --pkg ./internal/mosaic/ [--pkg ...]

Prints one line per step (PASS / FAIL / SKIP) followed by the tail of any failing
output. Exit code is 1 when any step fails. Never edits files.
"""
import argparse
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TAIL = 40


def run(args, timeout=1800):
    p = subprocess.run(args, cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace", timeout=timeout)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def changed_go_files():
    files = set()
    for cmd in (["git", "diff", "--name-only", "HEAD"],
                ["git", "ls-files", "--others", "--exclude-standard"]):
        code, out = run(cmd)
        if code == 0:
            files.update(l.strip() for l in out.splitlines() if l.strip().endswith(".go"))
    return sorted(f for f in files if os.path.exists(os.path.join(ROOT, f)))


def packages_of(files):
    pkgs = set()
    for f in files:
        d = os.path.dirname(f).replace("\\", "/")
        pkgs.add("./" + d + "/" if d else "./")
    return sorted(pkgs)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--all", action="store_true", help="vet and test ./...")
    ap.add_argument("--no-test", action="store_true")
    ap.add_argument("--pkg", action="append", default=[], help="extra package dir(s) to vet/test")
    a = ap.parse_args()

    results = []  # (step, status, detail)
    failed_output = []

    files = changed_go_files()
    pkgs = ["./..."] if a.all else sorted(set(packages_of(files) + a.pkg))

    # 1. gofmt on changed files
    if files:
        code, out = run(["gofmt", "-l"] + files)
        bad = [l for l in out.splitlines() if l.strip()]
        if code != 0 or bad:
            results.append(("gofmt", "FAIL", f"{len(bad)} file(s) need gofmt -w"))
            failed_output.append("gofmt -l:\n" + "\n".join(bad or [out]))
        else:
            results.append(("gofmt", "PASS", f"{len(files)} changed .go file(s)"))
    else:
        results.append(("gofmt", "SKIP", "no changed .go files"))

    # 2. build everything (cheap, catches cross-package breaks)
    code, out = run(["go", "build", "./..."])
    if code != 0:
        results.append(("build", "FAIL", "go build ./..."))
        failed_output.append("go build:\n" + "\n".join(out.splitlines()[-TAIL:]))
    else:
        results.append(("build", "PASS", "go build ./..."))

    # 3. vet changed packages
    if pkgs:
        code, out = run(["go", "vet"] + pkgs)
        if code != 0:
            results.append(("vet", "FAIL", " ".join(pkgs)))
            failed_output.append("go vet:\n" + "\n".join(out.splitlines()[-TAIL:]))
        else:
            results.append(("vet", "PASS", " ".join(pkgs)))
    else:
        results.append(("vet", "SKIP", "no changed packages"))

    # 4. test changed packages
    if a.no_test:
        results.append(("test", "SKIP", "--no-test"))
    elif pkgs:
        code, out = run(["go", "test", "-count=1"] + pkgs)
        lines = out.splitlines()
        summary = [l for l in lines if l.startswith(("ok", "FAIL", "---", "?"))]
        if code != 0:
            results.append(("test", "FAIL", " ".join(pkgs)))
            fails = [l for l in lines if "FAIL" in l or "panic" in l or l.startswith("    ")]
            failed_output.append("go test:\n" + "\n".join((fails or lines)[-TAIL:]))
        else:
            results.append(("test", "PASS", "; ".join(summary)[:200]))
    else:
        results.append(("test", "SKIP", "no changed packages"))

    width = max(len(s) for s, _, _ in results)
    for step, status, detail in results:
        print(f"{step.ljust(width)}  {status}  {detail}")
    for block in failed_output:
        print("\n" + block)
    sys.exit(1 if any(s == "FAIL" for _, s, _ in results) else 0)


if __name__ == "__main__":
    main()
