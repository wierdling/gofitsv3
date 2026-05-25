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


## Star removal / star mask goals

Implement star mask and starless narrowband-channel generation for already-aligned/drizzled FITS images.

The intended workflow is:

1. Normalize multiple aligned channels.
2. Build one shared star-detection image.
3. Estimate local background and local noise.
4. Detect compact bright sources.
5. Build a binary star mask.
6. Dilate the mask based on star size/brightness.
7. Feather the mask for soft recombination.
8. Inpaint masked pixels in each channel to create starless channels.
9. Extract star layers using original - starless.
10. Recombine nebula and stars with controlled star saturation.

Do not implement this as AI/ML star removal. Use deterministic image-processing methods.