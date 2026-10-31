---
name: check
description: Run the GoFitsV3 closing checks (gofmt on changed files, go build ./..., go vet and go test on the changed packages) and report a short PASS/FAIL table. Use before reporting any Go change complete, before /commit, and whenever the user asks to "run the checks" or "run the tests".
---

# /check

Run `python -I scripts/check.py` from the repository root and relay the result.

- Default scope is the packages that own changed `.go` files (vs HEAD, plus untracked files). Pass `--all` when the change touches shared code in `internal/fitsio`, `internal/models` or `internal/processing`, or when the user asks for the full suite. Pass `--pkg ./internal/x/` to add a package the diff does not show.
- The script never edits files. If gofmt fails, run `gofmt -w` on the listed files and rerun. If build, vet or tests fail, fix the cause; do not skip the step or rerun hoping it passes.
- Report one line per step as the script prints it, then only the failing lines from the tail. Do not paste the whole test log.
- Tests in `internal/processing` take several seconds and `internal/ui` builds the Fyne app headless; both are normal. Real-data tests are skipped unless their env vars are set (see `internal/starstretchpreview/realdata_test.go`).
- A PASS from this script does not cover behavior the user must see in the app (dialogs, previews, drizzle output on real data). Say so in the wrap-up when the change is user-visible.
