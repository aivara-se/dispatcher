---
name: review
description: How to review another agent's change, what the verdict must contain, and the fixed report schema that makes two reviews comparable.
when-to-use: A pull request or a task handoff is waiting on your review. Never for your own work — the author of a branch never reviews it.
---

# Review

## Do this, in order

1. Read the task or pull request body: what was claimed, and what the acceptance criteria are. The claim and the task are not always the same thing, and the difference is a finding.
2. Read the diff twice — once for what it does, once for what it does **not** do: the caller nobody updated, the test that would have caught it, the document it just made untrue, the error path that swallows.
3. Run it. Check out the branch, run the checks this repository gates on — whatever CI runs, where there is CI — and exercise the thing the change claims to fix. A reviewer who did not run the change is reading, not reviewing.
4. Test the claim, not the description: if the body says empty input is handled, feed it empty input; if it says the bug is fixed, reproduce the bug on the parent commit first.
5. Look at what the checks cannot see: silently swallowed errors, a public surface that grew, a dependency that appeared, a gate that was skipped and reported as passing, credentials or personal data in the diff.

## The verdict

Return exactly one, and put it on the first line:

- `approve` — you ran it and found nothing that must change before it lands.
- `request changes` — with the concrete change required, each item carrying file, line and the evidence that shows it.
- `block` — only when the decision is not yours: missing access, a product decision, an external service. Say who must decide.

**Never** approve a change you did not run, and **never** approve a change to a branch you wrote in this task. A review with no findings says so plainly; it does not invent findings to look thorough, and it does not pad with praise.

## The report

Lead with the verdict line, then the sections below, in this order. Two reviewers of the same diff produce comparable reports because they use the same shape. Every finding carries the evidence that shows it: a finding without evidence is an opinion, and opinions are not actionable.

### 1. Summary and code health audit

What the change does, in the reviewer's own words, and whether that matches what the body claimed. Then the one or two systemic risks in it: what would still matter six months from now.

### 2. Findings

Most severe first, each in this shape:

- **Location:** file and line, or the smallest unit that identifies it.
- **The violation:** the rule or principle broken — correctness, maintainability, the repository's own conventions.
- **The problem and impact:** how this degrades correctness, testability or maintenance cost, concretely.
- **The evidence:** the command, the input or the output that shows it.

Separate **must fix before this lands** from **worth doing later**. A review that cannot tell the two apart blocks work that should have landed and approves work that should not.

### 3. Target design

The shape the change should have instead: what moves where, what the boundary is, which finding it settles. One screen, not a redesign of the repository.

### 4. Phased plan

Only when changes are required, and only for the requested changes: a low-risk-first sequence, so the author can land the safe part without waiting on the decision in the risky part.

### 5. Blueprint

Only when a concrete transformation makes the finding clearer than prose does. Contrast the current shape with the intended one, in the repository's own language, over the smallest excerpt that shows it.

## What a review is not

- Not a second opinion on style the formatter already enforces.
- Not a redesign of the repository, or a request for work the task did not ask for.
- Not a rewrite: if the change can land with a small fix, request the small fix. If it cannot, say what makes it unsalvageable.
