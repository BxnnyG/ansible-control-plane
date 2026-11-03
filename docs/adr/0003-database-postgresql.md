# ADR-0003: PostgreSQL as the only state store and job queue

- Status: accepted
- Date: 2026-11-03

## Context
State (users, inventories, runs, host observations) and a reliable job queue are needed without adding Redis/RabbitMQ.

## Decision
PostgreSQL (current major, pinned image tag). Job queue via `SELECT … FOR UPDATE SKIP LOCKED`; live log fan-out via `LISTEN/NOTIFY`; run logs stored in chunks with a retention policy. Integration tests use a real Postgres (testcontainers).

## Consequences
One stateful component to back up. Log volume must be bounded (retention, compression).

## Alternatives
SQLite (simpler, but weaker concurrency and no LISTEN/NOTIFY); Postgres + Redis (extra component without need at this scale).
