# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

Teaching material for a Polytech DevOps course (the README and UI text are in French). It is a deliberately simple voting app that students containerize and deploy with Docker Compose, Kubernetes, and Terraform on Azure. Commits often line up with lab steps ("tp3", "tp4"). The app is meant to be simple, not a production-grade architecture, so keep changes minimal.

## Architecture / data flow

```
vote --RPUSH "votes"--> Valkey --BLPOP--> worker --UPSERT--> Postgres <--poll 1s-- result --SSE--> browser
```

All three services are written in Go. Each one is its own module (`microservices/vote/`, `microservices/worker/`, `microservices/result/`, each with a `go.mod`) and is a flat `main` package with its assets embedded via `//go:embed`. There is no shared code: helpers such as `getenv`, `serve` and `connectValkey` are duplicated on purpose so that each service builds and dockerizes on its own.

- **vote/**: reads or creates a `voter_id` cookie and pushes `{"voter_id","vote"}` JSON to the Valkey list `votes`. Valid votes are only `"a"` and `"b"`; `OPTION_A`/`OPTION_B` only change the labels. The page is `templates/index.html` (`html/template`).
- **worker/**: creates the `votes(id UNIQUE, vote)` table at startup, then pops from the list with `BLPOP` and runs `INSERT ... ON CONFLICT (id) DO UPDATE`, so there is one vote per voter. It serves a health endpoint at `:8080/healthz` that the k8s probes use.
- **result/**: connects to Postgres before it starts listening (it exits after 3 failed attempts). It queries the counts every second and fans them out to the `/scores` Server-Sent Events stream (`broadcast.go`, events `scores`/`dberror`). The frontend is vanilla JS (`views/app.js`) with no framework.

`monolith/` (also its own module) is a single-image variant: one server exposing `/vote/` and `/result/` (+ `/result/scores` SSE) that reads and writes Postgres directly, with no Valkey and no worker. It needs only `POSTGRES_URL` (plus `OPTION_A`/`OPTION_B`, `PORT` default 8080). Its pages are copies of the vote/result assets with relative paths. Compose profile `monolith`, port 5002 (the microservices have profile `microservices`; db is shared).

Paths below such as `vote/` are relative to `microservices/`.

## Configuration contract (env vars)

- `VALKEY_URL` (vote, worker): parsed with `valkey.ParseURL`. The schemes are `valkey://`/`redis://`, or `valkeys://`/`rediss://` for TLS.
- `POSTGRES_URL` (worker, result): parsed by pgx, for example `postgres://user:pass@db:5432/postgres`.
- `OPTION_A`, `OPTION_B` (vote), `PORT` (vote defaults to 8080, result to 4000). The images run as non-root (ko's default base), so services must not listen on ports below 1024.

`k8s/` and `terraform/` still use the old Python/.NET/Node-era names (`REDIS_CONNECTION_STRING`, `POSTGRESQL_CONNECTION_STRING`), Redis, and port 80 for vote. Migrating them is a separate step.

## Commands

Every make target takes an optional scope, `microservices` or `monolith` (e.g. `make dev monolith`); with no scope it applies to both. The scope words are read from `MAKECMDGOALS`. Compose services are selected via profiles.

Everything goes through the root `Makefile` (`make help`). Because each service is a separate module, the Makefile loops over `SERVICES` and runs each command from inside the service directory.

- `make dev`: ko-builds the images, runs `make up` (`docker compose up -d --wait` with the committed `compose.yaml`, then prints the vote :5000 and result :5001 URLs) and follows the logs. Ctrl+C leaves the stack running; `make down` stops it
- `make build` / `make push`: ko builds (all of `microservices/*` and `monolith`) `$(REGISTRY)/<svc>:$(TAG)`. `build` targets only the host platform and loads into Docker. `push` is multi-arch (`PLATFORMS`), refuses the default `TAG=dev`, and is what CI will call
- `make check`: lint + test + audit + tidy-check (the CI gate)
- `make build-go`, `make test`, `make lint` (golangci-lint with the root `.golangci.yml`), `make audit` (govulncheck), `make tidy`
- One test: `cd microservices/worker && go test -run TestDecodeBallot ./...`

ko, golangci-lint and govulncheck run through `go run` at pinned versions in the Makefile. Bump them there.

There are no Dockerfiles: ko builds the images, and the `//go:embed` assets live in the binary.

## Deployment

- **k8s/**: plain manifests that you apply with `kubectl apply -f k8s/`. They include Deployments for vote/worker/result, StatefulSets with Services for redis and Postgres, and images from `rgy.k8s.devops-svc-ag.com/polytech/*`. When you bump an app version, update the image tag here.
- **terraform/**: Azure (azurerm ~>4.0). It sets up the resource group, a VNet with a platform subnet and an App Service–delegated subnet, a Basic Redis cache behind a private endpoint with a private DNS zone, and a Linux Web App that runs only the `vote` image with VNet integration. Resource names get a random suffix. Run `terraform init/plan/apply` from `terraform/`.
