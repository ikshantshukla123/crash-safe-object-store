# 0001 — Record architecture decisions

Status: accepted · Date: 2026-10-01

## Context

This project is a learning playground for distributed-storage internals. The
hard part of such a system is not the code, it is the handful of decisions that
everything else hangs off. Six months later the code still reads fine but the
reasoning is gone, and the temptation is to "improve" something that was chosen
deliberately.

## Decision

Every locked architecture decision gets a numbered file in `docs/decisions/`.
Each one states the context, the decision, what it costs us, and what we
rejected. Files are append-only: to change a decision, add a new record that
supersedes the old one rather than editing history.

## Consequences

- A reviewer (or an AI agent) can learn the *why* without asking.
- "Is this allowed?" has a lookup instead of an argument.
- Small overhead per decision; only architecture-level choices get a record,
  not routine implementation choices.
