# Application de vote

Une application distribuée simple qui permet de voter !

## Architecture

```mermaid
flowchart LR
    user((Votant)) -->|HTTP| vote[vote<br/>Go]
    vote -->|RPUSH votes| valkey[(Valkey)]
    valkey -->|BLPOP votes| worker[worker<br/>Go]
    worker -->|INSERT / UPDATE| db[(PostgreSQL)]
    db -->|SELECT toutes les secondes| result[result<br/>Go]
    result -->|Server-Sent Events| viewer((Navigateur))
```

* Un front-end en Go qui permet le vote entre deux options
* Un Valkey qui collecte les nouveaux votes
* Un worker en Go qui consomme les votes
* Une base de données Postgres qui stocke les résultats
* Une web app en Go qui affiche les résultats en temps réel

## Configuration

Chaque service est un module Go indépendant (`vote/`, `worker/`, `result/`), configuré par variables d'environnement :

| Variable | Services | Exemple | Description |
| --- | --- | --- | --- |
| `VALKEY_URL` | vote, worker | `valkey://valkey:6379/0` | URL de connexion à Valkey (`valkeys://` ou `rediss://` pour TLS) |
| `POSTGRES_URL` | worker, result | `postgres://postgres:postgres@db:5432/postgres` | URL de connexion à PostgreSQL |
| `OPTION_A`, `OPTION_B` | vote | `Glaces`, `Sorbets` | Libellés des deux options (par défaut `Glaces` / `Sorbets`) |
| `PORT` | vote, result | `8080`, `4000` | Port d'écoute HTTP (par défaut `8080` pour vote, `4000` pour result) |

Le worker expose un endpoint de santé sur `:8080/healthz`.

## Construire et lancer

Tout passe par le `Makefile` (`make help` liste les cibles). Les images sont construites avec [ko](https://ko.build), sans Dockerfile, et nommées `$(REGISTRY)/<service>:$(TAG)` (par défaut `rgy.k8s.devops-svc-ag.com/polytech/<service>:dev`).

| Commande | Description |
| --- | --- |
| `make dev` | Construit les images et lance toute la stack avec `compose.yaml` (vote sur http://localhost:5000, result sur http://localhost:5001) |
| `make build` | Construit les images avec ko et les charge dans Docker |
| `make push REGISTRY=... TAG=...` | Construit et pousse les images multi-arch (`linux/amd64,linux/arm64` par défaut, voir `PLATFORMS`) vers un registre ; `TAG` est obligatoire |
| `make build-go` | Compile les binaires dans `bin/` |
| `make check` | Lance tout ce que la CI vérifie : `lint`, `test`, `audit`, `tidy-check` |
| `make up` / `make down` / `make logs` / `make clean` | Gestion de la stack compose |

ko, golangci-lint et govulncheck sont lancés via `go run` à des versions figées : seuls Go et Docker (avec le plugin compose) sont nécessaires.

Pour lancer un service hors conteneur :

```sh
export VALKEY_URL=valkey://localhost:6379/0
export POSTGRES_URL=postgres://postgres:postgres@localhost:5432/postgres
cd vote && go run .
```

## Notes

L'application de vote n'accepte qu'un seul vote par navigateur client. Elle n'enregistre pas de votes supplémentaires si un vote a déjà été soumis par un client.
Il ne s'agit pas d'un exemple d'application distribuée correctement architecturée et parfaitement conçue... c'est juste un
exemple simple des différents types de pièces que vous pourriez voir (files d'attente, données persistantes, etc.), et
comment les gérer dans Docker à un niveau basique.
