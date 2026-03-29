# Contributing to exam-controller

## Getting started

```sh
git clone git@github.com:rdrake/exam-controller.git
cd exam-controller
make setup
```

`make setup` installs git hooks that run automatically:

- **pre-commit** -- `make fmt-check vet` (fast, every commit)
- **pre-push** -- `make verify-fast` (lint, vulncheck, unit tests, helm verify)

## Development workflow

1. Create a branch off `main`.
2. Make your changes.
3. Run `make verify-fast` to catch issues early.
4. Run `make test` for the full integration suite with coverage.
5. Open a pull request.

## Make targets

| Target | What it does |
|---|---|
| `make setup` | One-time post-clone setup (installs git hooks) |
| `make verify-fast` | Fast preflight: generated files, fmt, vet, lint, vulncheck, unit tests, helm verify |
| `make test` | Full integration suite with envtest + 80% coverage gate |
| `make test-unit` | Unit tests only (no envtest) |
| `make test-e2e` | End-to-end tests on a Kind cluster |
| `make lint` | Run golangci-lint |
| `make lint-fix` | Run golangci-lint with auto-fix |
| `make vulncheck` | Run govulncheck against code and dependencies |
| `make build` | Build the manager binary |
| `make manifests` | Regenerate CRDs and RBAC from markers |
| `make generate` | Regenerate DeepCopy methods |
| `make check-generated` | Verify generated files are committed |
| `make helm-verify` | Lint and template-render the Helm chart |
| `make docker-build IMG=<tag>` | Build the container image |
| `make coverage` | Generate an HTML coverage report |

## Code conventions

- After editing `*_types.go`, run `make manifests generate`.
- After editing any `.go` file, run `make lint-fix test`.
- Label constants live in `internal/provisioner/resources.go` -- use them instead of raw strings.
- Never edit files under `config/crd/bases/` or `zz_generated.*.go` by hand.

## Tests

The project has three test layers:

- **Unit tests** -- Pure Go logic, no cluster required. Run with `make test-unit`.
- **Integration tests** -- Use envtest (in-process API server). Run with `make test` or `make test-integration`.
- **End-to-end tests** -- Deploy to a real Kind cluster. Run with `make test-e2e`.

Coverage must stay at or above 80%. The `make test` target enforces this automatically.

## CI pipeline

Pull requests run these workflows:

| Workflow | What it checks |
|---|---|
| **Preflight** | `make verify-fast` (generated files, fmt, vet, lint, vulncheck, unit tests, helm) |
| **Tests** | Full integration suite + coverage gate |
| **E2E Tests** | Kind cluster lifecycle tests |
| **Build** | Multi-arch Docker build + Trivy vulnerability scan |

Pushes to `main` run the same workflows. Tagged releases (`v*`) additionally publish:

- A GitHub Release with `install.yaml`
- A Helm chart OCI image to `ghcr.io`

## Dependencies

Dependencies are managed with `go mod`. Dependabot opens PRs weekly for Go modules, Docker base images, and GitHub Actions. Review and merge them promptly -- stale dependencies are how CVEs get in.

To update manually:

```sh
go get -u ./...
go mod tidy
make verify-fast
```

## Releasing

1. Ensure `main` is green.
2. Tag with a semver: `git tag v1.2.3 && git push origin v1.2.3`
3. The release workflow handles everything else.
