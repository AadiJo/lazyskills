## Cloned Dependency Source

Read-only dependency source repositories are available under
`.slim/clonedeps/repos/` for inspection. Do not edit these clones.

- `.slim/clonedeps/repos/vercel-labs__skills/` — `vercel-labs/skills` at `be0dd25b4a8665894a56f45ef582cc02ca802c39`; reference source for skill definitions and examples for the new project.

## Validation Before Push

Before pushing any branch, run the full local validation suite used by CI and fix failures first. At minimum, run `go test ./...` plus the repository's configured lint/static/dead-code checks from CI workflows. Do not push with known validation failures.
