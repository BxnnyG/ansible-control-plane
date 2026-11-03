# ADR-0002: Frontend with SvelteKit and Tailwind, embedded in the Go binary

- Status: proposed
- Date: 2026-11-03

## Context
A fast, modern UI (command palette, dark/light, live logs) is required; deployment should stay a single app container.

## Decision
SvelteKit (Svelte 5) with `adapter-static` as a single-page app, TypeScript, Tailwind CSS. The build output is embedded via `go:embed` and served by the app. API types are generated from an OpenAPI spec maintained in the backend.

## Consequences
No Node runtime in production. Server-side rendering is not used (fine for an authenticated admin tool).

## Alternatives
React/Next.js (larger bundle, SSR not needed); HTMX + Go templates (less suitable for a rich, keyboard-driven UI).
