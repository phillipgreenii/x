# Use Architecture Decision Records at Repository Root

**Status**: Accepted
**Date**: 2026-08-26
**Deciders**: Phillip Green II

## Context

`x` holds Go code shared across several sibling repos in this workspace (`phillipg-nix-repo-base`,
`phillipgreenii-nix-agent-support`, `phillipgreenii-nix-support-apps`, `phillipgreenii-nix-personal`,
`phillipg-nix-ziprecruiter`). Decisions made here — which implementation becomes canonical when
two existing ones are merged, module-boundary choices, versioning conventions — need a
discoverable, version-controlled home, matching the convention already used by every other repo
in this workspace.

## Decision

Architecture Decision Records live in `docs/adr/` at this repo's root, using the same
`NNNN-{short-title}.md` zero-padded sequential numbering as every sibling repo. See
`phillipg-nix-repo-base`'s `docs/adr/0000-use-architecture-decision-records.md` for the full
convention (naming, draft vs. accepted, cross-repo reference format) — this repo adopts it
unchanged, starting its own independent sequence at `0000`.

## Consequences

### Positive

- Consistent with every other repo in this workspace — no new convention to learn.
- Version-controlled alongside the code the decision affects.

### Negative

- Numbering is independent per repo; a cross-repo reference must name the repo.

## Related Decisions

See also: `phillipg-nix-repo-base` `docs/adr/0000-use-architecture-decision-records.md`
