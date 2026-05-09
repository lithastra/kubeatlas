# KubeAtlas Governance

This document describes how KubeAtlas is governed. It applies to
the project's source code, documentation, and community
infrastructure (issue tracker, discussions, releases). It does
not bind any organisation that uses KubeAtlas downstream — those
relationships are commercial, not governance.

The governance model is deliberately small. KubeAtlas is a
focused tool with a narrow scope, and the project values
predictability over process gymnastics. When this document and
practical reality disagree, fix this document; do not pretend
practice matches.

## Roles

There are three roles. All are open to everyone willing to do
the work; none is gated on employment, geography, or seniority.

### Contributor

Anyone who has merged a non-trivial change — code, tests,
documentation, a substantial review — is a contributor.
Contributors are listed implicitly through git history; there is
no separate roster. The list of contributor handles in
release notes is generated from `git log` at tag time.

Contributors:

- Open issues and pull requests.
- Review pull requests; reviews are advisory.
- Participate in discussions; discussion outcomes inform but do
  not bind maintainers.

### Maintainer

A maintainer is a contributor with merge rights and a sustained
record of stewardship. The current maintainer roster is in
[MAINTAINERS.md](./MAINTAINERS.md). Maintainers:

- Review and merge pull requests.
- Triage issues; assign labels, milestones, and ownership.
- Cut releases per [RELEASE.md](./RELEASE.md).
- Decide on technical direction within their area of focus.
- Bear responsibility when something they merged breaks.

A maintainer who steps back from active work moves to
**emeritus** status (see below).

### Emeritus maintainer

Maintainers who reduce involvement do not lose their place in
the project's history, but they do lose merge rights. Emeritus
maintainers retain full participation rights as contributors.
Re-promotion follows the same process as initial nomination.

## Decision-making

KubeAtlas uses **lazy consensus**: a proposal moves forward
unless someone with merge rights objects within a stated
window. This works for most changes — bug fixes, additive
features, doc improvements, refactors that don't change the
contract.

For higher-stakes decisions, the project uses **formal
decision** mode (see below).

### Lazy consensus

The default for pull requests:

1. Author opens a PR.
2. CI runs; reviewers comment; author iterates.
3. After at least one maintainer approval and CI green, the PR
   merges. There is no minimum wait time.
4. If a second maintainer raises an objection within one week of
   merge, the change is reverted and re-proposed for formal
   decision.

The one-week revert window is the only "wait" in the model. Most
changes never trigger it.

### Formal decision

Some decisions need explicit alignment, not just no-objection.
A change qualifies as a formal decision when **any maintainer**
flags it as such; common categories:

- Breaking changes to the v1 API surface (renaming a field,
  removing an endpoint).
- Removing a built-in extractor or significantly changing the
  edges it emits.
- Changing the project license or DCO requirement.
- Adopting a new runtime dependency that ships in the binary
  (excluding go.mod transitive deps that don't change shipped
  behaviour).
- Renaming the project, transferring the repo, or changing the
  default branch.
- Changes to this governance document itself.

Process:

1. The proposing maintainer opens a GitHub Discussion (or PR with
   discussion-only label) describing the change.
2. The discussion stays open for at least seven calendar days.
3. The decision passes when a majority of currently-active
   maintainers vote in favour. "Active" means the maintainer
   has opened, reviewed, or merged something in the prior 90
   days. If there is only one active maintainer, the maintainer
   posts the decision and the seven-day window doubles as a
   public-comment period; the maintainer must address every
   substantive objection in writing before the change lands.
4. Outcomes are recorded in the discussion thread and (for
   API/governance changes) in `CHANGELOG.md`.

A maintainer may abstain. Tie votes default to the status quo.

### Tie-breakers

When the maintainer pool is split, the tiebreaker is the
project's primary maintainer (currently a single person; see
MAINTAINERS.md). The primary maintainer's tiebreaker vote
counts as one vote for tally purposes; it does not override a
majority.

## Becoming a maintainer

Nomination is by an existing maintainer, based on:

- A sustained record of contribution (typically six months and
  multiple merged PRs across two or more subsystems).
- Demonstrated good judgment in review — leaving useful comments
  on others' PRs, not just merging their own.
- Public conduct that aligns with [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md).

The nominator opens a discussion or PR adding the candidate to
`MAINTAINERS.md`. The vote follows the formal-decision process.

A self-nomination is allowed but treated identically; the
nominee should still solicit a maintainer to second the
proposal so the discussion has at least one independent voice.

There is no quota or maximum maintainer count. The project
expects to remain small (single-digit maintainers) for the v1
trajectory; that's a feature, not an aspiration to grow into.

## Stepping down / emeritus transition

A maintainer can move to emeritus voluntarily at any time by
opening a PR against `MAINTAINERS.md`. The transition is
informational, not a vote.

The remaining maintainers may move an inactive maintainer to
emeritus when:

- The maintainer has not opened, reviewed, or merged anything
  in the prior six months, AND
- The remaining maintainers have made a good-faith attempt to
  reach the maintainer (issue ping, email if available) and
  received no response within thirty days.

This is a formal decision. The intent is keeping the active
roster honest, not punishing time off — emeritus is not a
demotion and re-promotion is straightforward.

## Code review

Every PR must:

- Pass CI.
- Have at least one maintainer approval before merging.
- Carry a [Conventional Commits](https://www.conventionalcommits.org/)
  title.
- Include a [DCO sign-off](./DCO) on every commit.

A maintainer may merge their own PR after self-review when:

- The change is documentation-only or test-only, AND
- CI is green.

For everything else, an independent reviewer is required, even
when the maintainer is the only active person on the project —
in that case the reviewer can be a contributor whose past work
suggests familiarity with the affected subsystem; the
maintainer must explicitly invite the review.

## Security disclosure

Security issues are reported privately first. See
[SECURITY.md](./SECURITY.md) for the canonical process; in
summary:

- Vulnerabilities go to the maintainers' email (listed in
  SECURITY.md), not the public issue tracker.
- Maintainers acknowledge within 72 hours and start triage.
- Coordinated disclosure: a private patch lands first, a CVE is
  filed, and a public advisory + new release ship together.
- Reporters get credit in the advisory unless they ask to be
  anonymous.

The project has not yet been through a public CVE; the process
is documented for the day one shows up rather than written
post-incident.

## CNCF / foundation status

KubeAtlas is **not** a CNCF Sandbox project, and applying is
not on the v1.x roadmap. The reasoning, recorded so future
maintainers do not relitigate it:

- The project's scope is narrow (a single CLI + server + chart,
  not a platform). Sandbox onboarding overhead — TOC review,
  due-diligence forms, working-group meetings — is large
  relative to that scope.
- The maintainer pool is small. Sandbox status raises the
  expectation of a multi-person maintainer cadence we are
  intentionally not building toward yet.
- Vendor neutrality is a Sandbox prerequisite. KubeAtlas is
  vendor-neutral in fact — no commercial sponsor, single primary
  maintainer — but we have not seen demand for the formal
  certification.

If demand changes (multiple downstream production users
explicitly ask, or maintainer count grows past a handful), the
maintainers may revisit. The decision to apply would be a formal
decision per the rules above.

## Relationship to other lithastra/* repositories

KubeAtlas lives under `github.com/lithastra/kubeatlas`. Sister
repositories under the same GitHub organisation
(`lithastra/kubeatlas-rules` for the OCI-distributed Rego rule
packs, plus future repos) follow the same governance model and
share the same maintainer roster by default. Each repository
has its own `MAINTAINERS.md` and may add maintainers
specifically for that repo.

Cross-repo dependencies are managed at the API boundary, not at
the maintainer level: rules-repo PRs that change rule-pack
schema must coordinate with the main repo's loader, and vice
versa.

## Trademark / branding

"KubeAtlas" is not a registered trademark. The project does not
police use of the name. The Apache 2.0 license governs the
code; the name is informally protected by community convention
(don't ship a fork called "KubeAtlas" with breaking
modifications). If trademark registration becomes useful, it
would be a formal decision.

## Changes to this document

Governance changes are formal decisions per the rules above.
Editorial changes (fixing typos, clarifying wording, updating
links) can land via lazy consensus.

The project tries to keep this document under 250 lines.
Process bloat is a smell; if a future amendment cannot fit, it
is probably trying to solve a problem this model was designed
to avoid.
