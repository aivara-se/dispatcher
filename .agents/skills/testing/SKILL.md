---
name: testing
description: What to test, where the tests live, how to run them, and what makes a test worth keeping.
when-to-use: Any change that adds or alters behaviour, any bug fix, and any change something else depends on. A behaviour change with no test is unfinished work.
---

# Testing

## What to write

- Every behaviour change gets a test that fails before the change and passes after it. For a bug fix, write the failing test first and keep it — the bug is not fixed if nothing would catch it coming back.
- Cover the happy path, the validation or error path, and at least one boundary: empty, maximum, malformed, duplicated.
- Test the contract, not the implementation: given this input, this output or this failure. A test that breaks when the internals are refactored is testing the wrong thing.
- Prefer parameterised or table-driven cases over copy-pasted blocks — one case per line, with the expectation visible.
- **Never** write a test that can pass for the wrong reason: no assertions that a call happened without checking what it returned, no `assert True`, no snapshot nobody reads.
- Deterministic above all: no wall-clock time, no randomness, no network, no dependence on file ordering or locale. Inject or freeze what you cannot control.
- Fast and local: mock the network and filesystem boundaries instead of calling them. A suite nobody runs is a suite that rots.

## Where they live

- Follow the placement convention the repository already uses; if it has none, put the test next to the code it verifies and say so in the pull request.
- Test data belongs with the test, named for the case it covers. Do not commit a fixture that another test would silently depend on.
- A harness that is code is code: written in the repository's own language and run with its package manager, not as a shell script of commands.

## Running them

- **Find out what this repository runs before you run anything.** There is no fixed shape and no fixed number: a repository may have one narrow test command, one gate that runs everything, a sequence of steps with no wrapper, or nothing at all. Read its manifest, its `scripts/`, its CI file and the instructions it already carries, and adapt to what is actually there — this skill deliberately names no command, because it cannot know.
- **Before a full handover, follow the same steps that validating a pull request follows** — the same commands, in the same order, on the tree the reviewer will get. Start narrow while you iterate; finish with everything the repository gates on, and read each result rather than the last exit code alone.
- Quote the command and the tree it ran against. A report without either is a claim, not a verification.
- If the repository has no test command at all, or has one that nothing runs — not your loop, not CI — say that in the handoff instead of claiming coverage.
- A test that passes only sometimes is a broken test. Fix it or delete it in the same change — **never** retry until green, and never mark a flaky test as expected failure to get past a gate.
