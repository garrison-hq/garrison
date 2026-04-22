---
name: Bug report
about: Something is broken in the supervisor, the migrations, or the build.
title: "[bug] "
labels: bug
---

## What happened

<!-- One or two sentences. What did you observe? -->

## What you expected

<!-- One sentence. What should have happened instead? -->

## Reproduction

Minimal steps from a fresh clone. The closer to a shell transcript,
the better.

```bash
# e.g.
git clone https://github.com/garrison-hq/garrison
cd garrison/supervisor
make build
...
```

## Environment

- Garrison commit / tag: <!-- `git rev-parse HEAD` -->
- OS: <!-- e.g. Linux 6.19.8 Fedora 43 -->
- Go version: <!-- `go version` -->
- Postgres version: <!-- `SELECT version();` -->
- Docker version (if relevant): <!-- `docker --version` -->
- How Postgres is running: <!-- local binary, docker container, managed? -->

## Logs

<!--
Supervisor logs (structured slog output). Include ~20 lines around
the failure. Scrub secrets.
-->

```json
```

## Relevant DB state

<!--
If applicable, the contents of event_outbox, agent_instances, or
tickets at the time of the bug. Use psql \x output so it's readable:

  SELECT * FROM agent_instances ORDER BY started_at DESC LIMIT 5 \gx
-->

```
```

## Additional context

<!-- Anything else. Linked logs, related issues, hypothesis. -->
