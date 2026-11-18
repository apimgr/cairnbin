# PWA update recovery alignment

Implementation order: update lifecycle and safe cache behavior first; then page handover handling; then regression coverage; verify against AI.md.

- [ ] Make worker installation, precaching, navigation fetch, and cache reads self-healing and safe from redirect/error replay.
- [ ] Make the update prompt reload exactly once only after acceptance, with a five-second takeover fallback.
- [ ] Add regression coverage for worker output and page update behavior.
