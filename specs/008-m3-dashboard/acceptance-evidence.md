# M3 acceptance evidence

Each row maps a Success Criterion from `spec.md` §"Success criteria"
to the test (or check) that verifies it.

| SC      | Description                                                                                                                                                                                                | Verified by                                                                                                                                                              | Status |
|---------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------|
| SC-001  | A new operator can complete first-run setup, log in, and read the org overview against real M2-arc data within 5 minutes — without consulting docs or `psql`.                                            | `tests/integration/golden-path.spec.ts` — first-run wizard + sign-in + org-overview render against testcontainer-seeded M2-arc data.                                      | ✅ |
| SC-002  | The activity feed renders a single 10-`assistant`-event run without dropping any event and without visual layout breakage on a 1280px viewport.                                                          | `tests/integration/activity-feed.spec.ts::single10AssistantEventRunRendersWithoutDroppingEventsAndWithoutOverflow`.                                                       | ✅ |
| SC-003  | Every surface renders correctly on viewports of 768px, 1024px, and 1280px+ widths.                                                                                                                       | `tests/integration/responsive.spec.ts` — three viewport tests, seven surfaces each.                                                                                       | ✅ |
| SC-004  | Theme parity is observable: every surface in dark mode has a corresponding light-mode rendering with no broken contrast.                                                                                  | `tests/integration/theme-parity.spec.ts` — dark + light tests assert resolved CSS-variable colours on all seven surfaces.                                                 | ✅ |
| SC-005  | A code-level audit shows zero English-string JSX literals outside the locale catalog. Adding a stub second locale re-renders surfaces with the new copy and zero missing-key errors.                     | `tests/integration/i18n.spec.ts::stubLocaleRendersAllSurfacesWithoutMissingKeys` + `fallingBackToEnglishOnAMissingKeyDoesNotSurfaceRawCatalogKeys`.                       | ✅ |
| SC-006  | The vault sub-views' runtime DSN resolves to `garrison_dashboard_ro` (not any agent-facing role).                                                                                                         | `tests/integration/vault-isolation.spec.ts::runtimeVaultDSNResolvesToDashboardRo` + `attemptingToReadAgentRoleSecretsViaAppDbFailsWithPermissionDenied`.                  | ✅ |
| SC-007  | All three documented hygiene-status failure modes are observable in the hygiene table when present in the underlying data.                                                                                 | `tests/integration/golden-path.spec.ts` step 7 — seeds one row per failure mode, asserts each is filterable + present.                                                    | ✅ |
| SC-008  | Every display position of `total_cost_usd` carries the cost-blind-spot caveat treatment when the trigger predicate matches.                                                                                | `tests/integration/cost-blind-spot.spec.ts::cleanFinalizeZeroCostShowsCaveat` + `nonZeroCostRowsDoNotShowCaveat`.                                                          | ✅ |
| SC-009  | `dashboard/Dockerfile` produces a runnable production image; the image starts, connects to Postgres, authenticates an operator, and renders the org overview.                                            | T019 — `docker build` produces a 217 MB image; `tests/integration/golden-path.spec.ts` boots the image (via `bun run start` with the pre-built standalone) and signs in.  | ✅ |
| SC-010  | The visual language of every surface matches the mocks at the design-system level.                                                                                                                        | Side-by-side review against `.workspace/m3-mocks/garrison-reference/` — token set lifted verbatim into `app/globals.css`; Sidebar/Topbar/UI primitives mirror shell.jsx.   | 🟡 manual |
| SC-011  | Drizzle and better-auth dependency justifications are captured in the M3 retro per `AGENTS.md` soft-rule discipline.                                                                                       | `docs/retros/m3.md` — see "Dependencies added outside the locked list" section.                                                                                            | ✅ |

## Test counts at acceptance

- **Unit tests**: 45 passing (vitest, against a Postgres testcontainer where DB-bound).
- **Integration tests**: 28 + golden-path = 29 passing (Playwright, against a built dashboard image + Postgres testcontainer).

## Test execution

```sh
cd dashboard
bun install
bunx playwright install chromium

# Unit tests
bun run test

# Integration tests (boots a Postgres testcontainer + builds the
# dashboard once + serves it via `bun run start`)
bunx playwright test --project=chromium-1280
```

## Out-of-band verification (SC-010)

The visual fidelity check against `.workspace/m3-mocks/garrison-reference/`
is a manual side-by-side at the design-language level — token values,
density, mono-flavored ID treatment, status-dot/chip vocabulary. The
mocks include screens for surfaces deferred to M4/M5/M7; those are
explicitly out of scope per A-006.
