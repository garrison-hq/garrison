# Acme Aerospace — Company Context

> **Mission**: design, manufacture, and operate the next generation of
> reusable orbital launch vehicles for commercial logistics customers.
> All strategic decisions must trace to "does this make us faster,
> cheaper, or safer at putting payloads in low-earth orbit?"

## Strategic posture (Q2 2026)

- **Q1 outcome**: 4 orbital launches; 2 commercial-payload missions
  closed at $14M each; 1 booster lost on splashdown recovery (root
  cause: salt-water ingress in the third-stage avionics bay; fix
  rolling out in Q2's Block 5 hardware).
- **Q2 priorities, in order**:
  1. Block 5 hardware certification (depends on FAA review timeline;
     6-week buffer assumed).
  2. Customer pipeline: target 6 signed launch contracts by end of Q2,
     2 of which must be government rideshare to demonstrate the
     pipeline isn't single-thread on commercial telco.
  3. Hire: VP of Mission Operations + 4 senior propulsion engineers
     (the Q1 hiring bar held at "10+ years closed-loop combustion
     experience"; do not relax).
- **Out of scope this quarter**: lunar mission planning, customer
  discovery for crewed missions, orbital refueling demos. All three
  are Q4-or-later strategic pivots; bringing them up in standups
  derails focus.

## Operating cadence

- **Weekly all-hands**: Mondays 09:00 PT, 30 minutes. CEO leads.
  Agenda: launch schedule, customer-pipeline movement, hardware-line
  blockers, hire status. No technical deep-dives.
- **Engineering review**: Wednesdays 14:00 PT, 90 minutes. CTO leads.
  Agenda: architectural decisions, post-mortems, capacity planning.
  This is where "do we ship Block 5 with the new pintle injector or
  hold for Block 5.1?" gets decided — not in Slack.
- **Customer pipeline review**: Fridays 10:00 PT, 60 minutes. Head of
  Sales leads. Agenda: deal stages, churn risk, contract redlines.

## Decision-making

- **CEO retains**: hiring decisions above the senior-IC level, launch
  go/no-go calls, customer-contract approvals over $10M, all
  regulatory-compliance escalations (FAA, ITAR, export controls).
- **CTO retains**: hardware architecture, supplier selection, quality
  gates, all engineer hires up to senior level.
- **Head of Mission Operations retains**: launch-day operational calls,
  range-safety coordination, recovery-asset deployment.
- **Default delegation principle**: if a decision is reversible inside
  72 hours and costs less than $50K to undo, the team owns it. If it's
  one-way or expensive to undo, escalate.

## Cultural norms

- **Direct disagreement is encouraged in meetings; criticism behind
  someone's back is grounds for a private conversation.** A bad call
  caught in a meeting is half the cost of one shipped.
- **The launch schedule is sacred.** Anything that pushes a launch
  date by more than 5 days requires a CEO-level explanation.
  Anything that pushes it by 24 hours requires the launch director's
  sign-off.
- **Documentation is part of the work, not a follow-up.** Engineering
  ADRs are landed in the same PR as the change they describe;
  retros are written within 48 hours of the incident or milestone.

## Cross-functional dependencies

- **Manufacturing → Engineering**: weekly hand-off Thursdays. Any
  engineering change affecting parts already in fab requires a 2-week
  notice or a documented exception.
- **Mission Ops → Engineering**: each launch carries a "lessons
  learned" pass written by Mission Ops within 7 days of recovery; the
  next sprint's engineering planning treats those items as priority-0
  backlog candidates.
- **Sales → Mission Ops**: customer manifest finalisation is T-30
  days. Past T-30 the manifest is locked; changes inside the window
  cost the customer a slot-shuffle fee.

---

*Last edit: 2026-04-30 by CEO. Prior edits in MinIO object versioning
(post-M5.5 once versioning lands).*
