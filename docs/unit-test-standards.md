# Unit Test Standards

## Purpose
Define what counts as a good unit test in this repository so contributors and agents apply the same standard when adding, reviewing, or expanding test coverage.

## What A Good Unit Test Is
- Tests one unit of behavior at a time.
- Uses a clear name that states the scenario and expected result.
- Is deterministic and produces the same result on every run.
- Verifies observable behavior and outputs rather than incidental implementation details.
- Keeps setup small and focused on the behavior under test.
- Fails for one clear reason so regressions are easy to diagnose.
- Covers the success path, relevant edge cases, and expected failure paths for non-trivial logic.
- Protects against realistic regressions instead of duplicating coverage that already exists elsewhere.

## What A Good Unit Test Is Not
- A broad integration test that exercises many systems at once.
- A flaky test that depends on timing, random values without control, network access, or machine-specific state.
- A test that only proves the current implementation shape instead of the intended behavior.
- A large fixture-heavy test when a small focused case would cover the same logic.
- A redundant test that adds maintenance cost without improving confidence.

## When Code Needs Unit Tests
- Logic-heavy exported functions should have unit tests.
- Parsing, transformation, math, image-processing, normalization, and decision-making code should have unit tests.
- Bug fixes should include a regression test whenever practical.
- Branching behavior should be covered by tests for each meaningful branch.
- Public helpers used across packages should usually have direct tests.

## When Unit Tests May Not Be Needed
- Trivial pass-through code with no meaningful logic may not need direct unit tests if covered elsewhere.
- Thin UI or wiring code may be better covered by higher-level tests if unit tests would only mirror implementation details.
- Generated code should only be tested if the generation output is part of the task.

## Test Design Rules
- Prefer table-driven tests when the same behavior must be checked across multiple scenarios.
- Use explicit inputs and expected outputs.
- Keep fixtures realistic but minimal.
- Avoid hidden shared mutable state between tests.
- Prefer dependency injection, small helpers, or extracted pure logic over hard-to-test code paths.
- Use regression tests that reproduce the specific bug before proving the fix.

## Review Checklist
A unit test is good enough to merge when it:
- clearly states the behavior being protected
- would fail if the target behavior regressed
- is stable and fast
- keeps scope narrow
- adds meaningful coverage rather than noise

## Agent Guidance
When asked whether a file needs more unit tests, evaluate it against this document. Prefer the smallest additional test set that covers untested meaningful behavior.
