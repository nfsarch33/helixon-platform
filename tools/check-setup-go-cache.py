#!/usr/bin/env python3
"""check-setup-go-cache — fail if any actions/setup-go step leaves caching on.

`actions/setup-go@v5` defaults its `cache` input to **true**, so an *absent*
`cache:` key means caching is ON. Its post-job cache-save runs

    /usr/bin/tar --posix -cf cache.tzst --use-compress-program zstdmt ...

and on this repository's sole self-hosted runner that tar enters
uninterruptible sleep (`STAT=D`). Signals are inert in D, so it
cannot be killed, only waited out — it holds the runner and every job in the
repository queues behind it. One such tar held the runner for 5h38m on
2026-08-30 with nine runs stacked behind it, and another was caught live the
same morning at 20+ minutes.

So `cache: false` here is not an optimisation, it is an availability control.
The cache buys nothing on a persistent runner anyway: `~/go` survives between
jobs.

This checker walks the *parsed* YAML (jobs -> steps -> `with`) rather than
grepping. A grep for `cache: false` cannot tell which step a matching line
belongs to, nor that it sits under that step's `with:` rather than under some
neighbouring key, so it reports a green for a workflow where the pin landed on
the wrong step.

It also fails when it finds **zero** `actions/setup-go` steps. Without that, the
gate would pass vacuously the moment a refactor moved, renamed or broke the
workflows it is supposed to be reading, and would keep reporting green while
protecting nothing.

Scope, stated so the boundary is not mistaken for coverage: this reads workflow
files in `.github/workflows/` and walks `jobs -> steps`. A composite action
(`.github/actions/*/action.yml`, whose steps live under `runs.steps`) would not
be reached. There are none in this repository today — if one is added and it
sets Go up, extend this walk rather than assuming the gate already covers it.

Usage:
    check-setup-go-cache.py [--workflow-dir DIR] [--repo-root PATH]
    check-setup-go-cache.py --self-test
    check-setup-go-cache.py --mutation-test

Exit codes:
    0  every actions/setup-go step pins `cache: false`, and at least one exists
    1  a violation, zero setup-go steps found, or a workflow that would not parse
    2  usage error, or PyYAML is not installed
"""
from __future__ import annotations

import argparse
import re
import shutil
import sys
import tempfile
from pathlib import Path
from typing import Iterator, NamedTuple

try:
    import yaml
except ModuleNotFoundError:  # pragma: no cover - exercised only on a bare runner
    sys.stderr.write(
        "check-setup-go-cache: PyYAML is required (pip install pyyaml).\n"
        "This gate parses workflow YAML on purpose; it does not fall back to a\n"
        "grep, because a grep cannot tell which step a `cache:` line belongs to.\n"
    )
    raise SystemExit(2)

# Matched as a prefix, so `actions/setup-go@v5`, a full-SHA pin and any future
# major all count. Over-matching is fail-safe here: the worst case is that an
# unrelated action gets asked to pin `cache: false` too.
ACTION_PREFIX = "actions/setup-go"

DEFAULT_WORKFLOW_DIR = Path(".github/workflows")
WORKFLOW_SUFFIXES = (".yml", ".yaml")


class Violation(NamedTuple):
    """One setup-go step that does not pin `cache: false`."""

    workflow: str
    job: str
    step: str
    reason: str

    def render(self) -> str:
        return f"{self.workflow}: job `{self.job}` -> {self.step}: {self.reason}"


def _step_label(index: int, step: dict) -> str:
    """A human-locatable name for a step, since steps are often unnamed."""
    name = step.get("name")
    if isinstance(name, str) and name.strip():
        return f"step {index} ({name.strip()})"
    uses = step.get("uses")
    if isinstance(uses, str) and uses.strip():
        return f"step {index} (uses: {uses.strip()})"
    return f"step {index}"


def iter_setup_go_steps(doc: object) -> Iterator[tuple[str, str, dict]]:
    """Yield (job_name, step_label, step) for every actions/setup-go step.

    Tolerates every shape a workflow can legally take: a job that calls a
    reusable workflow has a top-level `uses:` and no `steps:` at all, and a
    `steps:` list can contain nulls. None of those are violations, so they are
    skipped rather than reported.
    """
    if not isinstance(doc, dict):
        return
    jobs = doc.get("jobs")
    if not isinstance(jobs, dict):
        return
    for job_name, job in jobs.items():
        if not isinstance(job, dict):
            continue
        steps = job.get("steps")
        if not isinstance(steps, list):
            continue
        for index, step in enumerate(steps):
            if not isinstance(step, dict):
                continue
            uses = step.get("uses")
            if not isinstance(uses, str):
                continue
            if not uses.strip().startswith(ACTION_PREFIX):
                continue
            yield str(job_name), _step_label(index, step), step


def check_step(step: dict) -> str | None:
    """Return a failure reason for a setup-go step, or None if it is compliant."""
    with_block = step.get("with")
    if not isinstance(with_block, dict):
        return (
            "no `with:` block, so `cache` is unset and setup-go@v5 defaults it "
            "to true — add `cache: false`"
        )
    if "cache" not in with_block:
        return (
            "`with:` has no `cache` key, so setup-go@v5 defaults it to true — "
            "add `cache: false`"
        )
    value = with_block["cache"]
    if value is False:
        return None
    if isinstance(value, str):
        return (
            f"`cache: {value!r}` is a quoted string, not the YAML boolean. Use "
            "the unquoted `cache: false` so this stays one canonical, greppable "
            "form"
        )
    return f"`cache` is {value!r}, must be the YAML boolean false"


def check_document(workflow: str, doc: object) -> tuple[list[Violation], int]:
    """Check one parsed workflow. Returns (violations, setup_go_steps_seen)."""
    violations: list[Violation] = []
    seen = 0
    for job_name, step_label, step in iter_setup_go_steps(doc):
        seen += 1
        reason = check_step(step)
        if reason is not None:
            violations.append(Violation(workflow, job_name, step_label, reason))
    return violations, seen


def check_directory(workflow_dir: Path) -> tuple[list[Violation], int, list[str]]:
    """Check every workflow in a directory.

    Returns (violations, setup_go_steps_seen, hard_errors). A workflow that does
    not parse is a hard error, never a silent skip — an unreadable workflow is
    exactly where an unpinned step would hide.
    """
    violations: list[Violation] = []
    errors: list[str] = []
    seen = 0

    if not workflow_dir.is_dir():
        errors.append(f"{workflow_dir}: not a directory")
        return violations, seen, errors

    paths = sorted(
        p for p in workflow_dir.iterdir()
        if p.is_file() and p.suffix in WORKFLOW_SUFFIXES
    )
    if not paths:
        errors.append(f"{workflow_dir}: contains no .yml/.yaml workflow files")
        return violations, seen, errors

    for path in paths:
        try:
            doc = yaml.safe_load(path.read_text(encoding="utf-8"))
        except (yaml.YAMLError, OSError) as exc:
            errors.append(f"{path}: could not be parsed: {exc}")
            continue
        file_violations, file_seen = check_document(str(path), doc)
        violations.extend(file_violations)
        seen += file_seen

    return violations, seen, errors


# --------------------------------------------------------------------------
# Self-test
#
# The gate's own positive control, run in CI on every invocation. A gate that
# has only ever been observed passing has not been shown to be capable of
# failing; these fixtures pin both directions in the repository itself, so a
# refactor that quietly defangs the checker turns the gate red instead of
# leaving a green that asserts nothing.
# --------------------------------------------------------------------------

_COMPLIANT = """
jobs:
  build:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: false
"""

_MISSING_CACHE_KEY = """
jobs:
  build:
    steps:
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
"""

_NO_WITH_BLOCK = """
jobs:
  build:
    steps:
      - uses: actions/setup-go@v5
"""

_CACHE_TRUE = """
jobs:
  build:
    steps:
      - uses: actions/setup-go@v5
        with:
          cache: true
"""

_CACHE_QUOTED_FALSE = """
jobs:
  build:
    steps:
      - uses: actions/setup-go@v5
        with:
          cache: "false"
"""

# The exact defect a grep cannot see: `cache: false` is present in the file, and
# is even under a `with:` — just not this step's. A line-oriented check reports
# green here.
_PIN_ON_THE_WRONG_STEP = """
jobs:
  build:
    steps:
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: actions/setup-node@v4
        with:
          cache: false
"""

_SECOND_STEP_UNPINNED = """
jobs:
  build:
    steps:
      - uses: actions/setup-go@v5
        with:
          cache: false
  release:
    steps:
      - uses: actions/setup-go@v5
        with:
          cache: true
"""

# Shapes that must not crash the walk and must not be reported as violations.
_NO_SETUP_GO = """
jobs:
  lint:
    steps:
      - uses: actions/checkout@v4
      - run: make lint
"""

_REUSABLE_WORKFLOW_CALL = """
jobs:
  delegated:
    uses: ./.github/workflows/other.yml
    secrets: inherit
"""

_ODD_BUT_LEGAL = """
jobs:
  weird:
    steps:
      -
      - run: echo no uses key
"""

_SELF_TEST_CASES = [
    # (name, yaml, expected_violations, expected_setup_go_steps_seen)
    ("compliant", _COMPLIANT, 0, 1),
    ("missing cache key", _MISSING_CACHE_KEY, 1, 1),
    ("no with block", _NO_WITH_BLOCK, 1, 1),
    ("cache: true", _CACHE_TRUE, 1, 1),
    ("cache: \"false\" (quoted)", _CACHE_QUOTED_FALSE, 1, 1),
    ("pin on the wrong step", _PIN_ON_THE_WRONG_STEP, 1, 1),
    ("second step unpinned", _SECOND_STEP_UNPINNED, 1, 2),
    ("no setup-go at all", _NO_SETUP_GO, 0, 0),
    ("reusable workflow call", _REUSABLE_WORKFLOW_CALL, 0, 0),
    ("null and bare steps", _ODD_BUT_LEGAL, 0, 0),
]


def run_self_test() -> int:
    failures = 0
    for name, text, want_violations, want_seen in _SELF_TEST_CASES:
        doc = yaml.safe_load(text)
        violations, seen = check_document(f"<self-test: {name}>", doc)
        got_violations = len(violations)
        if got_violations != want_violations or seen != want_seen:
            failures += 1
            print(
                f"FAIL {name}: got {got_violations} violation(s) / {seen} "
                f"setup-go step(s), want {want_violations} / {want_seen}"
            )
            for violation in violations:
                print(f"       {violation.render()}")
        else:
            print(f"ok   {name}: {got_violations} violation(s), {seen} setup-go step(s)")

    # The anti-vacuous rule is itself part of the contract, so assert it here
    # rather than trusting that main() still applies it.
    _, seen = check_document("<self-test: vacuity>", yaml.safe_load(_NO_SETUP_GO))
    if seen != 0:
        failures += 1
        print("FAIL vacuity: a workflow with no setup-go step must report zero seen")

    total = len(_SELF_TEST_CASES) + 1
    if failures:
        print(f"\nself-test: {total - failures}/{total} passed, {failures} FAILED")
        return 1
    print(f"\nself-test: {total}/{total} passed")
    return 0


# --------------------------------------------------------------------------
# Mutation test
#
# The self-test above proves the checker fails on synthetic fixtures. This
# proves it fails on THIS repository's real workflows: it deletes each
# `cache: false` pin in turn, and flips each one to true in turn, and requires
# a violation every time. If someone rewrites the workflows in a shape this
# walk cannot see — a matrix, an anchor, a composite action — the pins stop
# being reachable and this goes red, instead of the gate quietly inspecting
# nothing.
# --------------------------------------------------------------------------

PIN_RE = re.compile(r"^\s*cache:\s*false\s*$")


def _find_pins(workflow_dir: Path) -> list[tuple[Path, int]]:
    pins: list[tuple[Path, int]] = []
    for path in sorted(workflow_dir.iterdir()):
        if not path.is_file() or path.suffix not in WORKFLOW_SUFFIXES:
            continue
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines()):
            if PIN_RE.match(line):
                pins.append((path, lineno))
    return pins


def run_mutation_test(workflow_dir: Path) -> int:
    failures = 0
    checks = 0

    def report(name: str, ok: bool, detail: str) -> None:
        nonlocal failures, checks
        checks += 1
        if ok:
            print(f"ok   {name}: {detail}")
        else:
            failures += 1
            print(f"FAIL {name}: {detail}")

    violations, seen, errors = check_directory(workflow_dir)
    report(
        "pristine tree is clean",
        not violations and not errors and seen > 0,
        f"{len(violations)} violation(s), {len(errors)} error(s), {seen} setup-go step(s)",
    )
    if errors or violations:
        print("\nmutation test: refusing to mutate an already-failing tree")
        return 1

    pins = _find_pins(workflow_dir)
    print(f"\nmutating {len(pins)} `cache: false` pin(s):")
    for path, lineno in pins:
        print(f"  {path.name}:{lineno + 1}")
    print()

    if len(pins) < seen:
        report(
            "every setup-go step has a mutatable pin",
            False,
            f"found {len(pins)} pin line(s) but {seen} setup-go step(s) — a pin is "
            "not on its own line, so this control cannot reach it",
        )

    with tempfile.TemporaryDirectory() as tmpdir:
        tmp = Path(tmpdir)
        for index, (path, lineno) in enumerate(pins):
            for label, mutate in (
                ("delete", lambda lines, n: lines[:n] + lines[n + 1:]),
                ("flip to true", lambda lines, n: lines[:n]
                    + [lines[n].replace("false", "true")] + lines[n + 1:]),
            ):
                dest = tmp / f"m{index}-{label.split()[0]}"
                shutil.copytree(workflow_dir, dest)
                target = dest / path.name
                lines = target.read_text(encoding="utf-8").splitlines()
                target.write_text(
                    "\n".join(mutate(lines, lineno)) + "\n", encoding="utf-8"
                )
                mutated, _, mutated_errors = check_directory(dest)
                report(
                    f"{label} {path.name}:{lineno + 1} is caught",
                    bool(mutated) and not mutated_errors,
                    f"{len(mutated)} violation(s), {len(mutated_errors)} parse error(s)",
                )
                shutil.rmtree(dest)

        empty = tmp / "empty"
        empty.mkdir()
        _, empty_seen, empty_errors = check_directory(empty)
        report(
            "an empty workflow dir cannot pass vacuously",
            empty_seen == 0 and bool(empty_errors),
            f"{empty_seen} setup-go step(s), {len(empty_errors)} error(s)",
        )

    if failures:
        print(f"\nmutation test: {checks - failures}/{checks} passed, {failures} FAILED")
        return 1
    print(f"\nmutation test: {checks}/{checks} passed")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=(
            "Fail if any actions/setup-go step in the workflows leaves setup-go's "
            "cache enabled. See the module docstring for why that wedges the "
            "self-hosted runner."
        )
    )
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=Path(__file__).resolve().parent.parent,
        help="Repository root (default: the parent of this script's directory).",
    )
    parser.add_argument(
        "--workflow-dir",
        type=Path,
        default=None,
        help=f"Workflow directory (default: <repo-root>/{DEFAULT_WORKFLOW_DIR}).",
    )
    parser.add_argument(
        "--self-test",
        action="store_true",
        help="Run the checker against built-in fixtures and exit. Proves the "
             "checker can fail, so a passing gate means something.",
    )
    parser.add_argument(
        "--mutation-test",
        action="store_true",
        help="Delete and flip each real `cache: false` pin in a scratch copy of "
             "the workflows and require a violation every time. Proves the gate "
             "can fail against THIS repository, not just against fixtures.",
    )
    args = parser.parse_args(argv)

    if args.self_test and args.mutation_test:
        parser.error("--self-test and --mutation-test are mutually exclusive")

    if args.self_test:
        return run_self_test()

    workflow_dir = args.workflow_dir or (args.repo_root / DEFAULT_WORKFLOW_DIR)

    if args.mutation_test:
        if not workflow_dir.is_dir():
            print(f"::error::check-setup-go-cache: {workflow_dir}: not a directory")
            return 1
        return run_mutation_test(workflow_dir)

    violations, seen, errors = check_directory(workflow_dir)

    for error in errors:
        print(f"::error::check-setup-go-cache: {error}")

    for violation in violations:
        print(f"::error::setup-go cache is not pinned off — {violation.render()}")

    if errors or violations:
        print(
            f"\ncheck-setup-go-cache: FAILED — {len(violations)} unpinned "
            f"actions/setup-go step(s), {len(errors)} unreadable workflow(s), "
            f"across {seen} setup-go step(s) inspected in {workflow_dir}."
        )
        print(
            "Every actions/setup-go step must carry `cache: false` under `with:`. "
            "setup-go@v5 defaults cache to true, and its post-job cache-save tar "
            "wedges this repository's only self-hosted runner in uninterruptible "
            "sleep."
        )
        return 1

    if seen == 0:
        print(
            "::error::check-setup-go-cache: found ZERO actions/setup-go steps in "
            f"{workflow_dir}. Refusing to pass vacuously — either the workflows "
            "moved and this gate is now reading the wrong place, or they no "
            "longer set up Go and this gate should be retired deliberately."
        )
        return 1

    print(
        f"check-setup-go-cache: PASSED — {seen} actions/setup-go step(s) "
        f"inspected in {workflow_dir}, all pinned `cache: false`."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
