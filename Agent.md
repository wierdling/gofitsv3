# AGENTS.md

## Project
Cross-platform desktop app in Go with a frontend UI.

## Goals
- Keep code portable across Windows, Linux, and macOS.
- Prefer small, reviewable commits.
- Preserve clear separation between backend and UI.

## Commands
- Install deps: ...
- Run dev build: ...
- Run tests: ...
- Lint: ...
- Package app: ...

## Structure
- /cmd = app entrypoints
- /internal = backend logic
- /ui = frontend
- /testdata = sample files

## Rules
- Ask before adding new dependencies.
- Do not rename public APIs unless necessary.
- Keep image-processing code benchmarkable.
- Add tests for new backend behavior when feasible.
- Keep backend code segregated by function (file loading/saving, image processing, etc.).
- Use go channels where appropriate to speed up processing.

## Definition of done
- Code builds
- Relevant tests pass
- No lint errors
- Brief summary of changes and tradeoffs