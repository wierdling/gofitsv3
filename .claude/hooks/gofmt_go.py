"""PostToolUse hook (Edit|Write): gofmt the Go file that was just edited.

Removes the "imported and not used" / formatting round trips by formatting the
touched file immediately. Uses goimports when it is on PATH (fixes imports too),
otherwise gofmt. Only touches .go files inside the project. Silent on success and
never fails the tool call.
"""
import json
import os
import shutil
import subprocess
import sys


def main():
    try:
        data = json.load(sys.stdin)
    except Exception:
        return
    tool_input = data.get("tool_input") or {}
    path = tool_input.get("file_path") or tool_input.get("path")
    if not isinstance(path, str) or not path.endswith(".go"):
        return
    root = os.environ.get("CLAUDE_PROJECT_DIR") or os.getcwd()
    try:
        rel = os.path.relpath(os.path.abspath(path), os.path.abspath(root))
    except ValueError:
        return
    if rel.startswith("..") or not os.path.isfile(path):
        return
    tool = shutil.which("goimports") or shutil.which("gofmt")
    if not tool:
        return
    try:
        subprocess.run([tool, "-w", path], capture_output=True, timeout=15)
    except Exception:
        pass


if __name__ == "__main__":
    main()
