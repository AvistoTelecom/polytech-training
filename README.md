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
| `PORT` | vote, result | `80`, `4000` | Port d'écoute HTTP (par défaut `80` pour vote, `4000` pour result) |

Le worker expose un endpoint de santé sur `:8080/healthz`.

## Lancer en local

```sh
export VALKEY_URL=valkey://localhost:6379/0
export POSTGRES_URL=postgres://postgres:postgres@localhost:5432/postgres

(cd vote && PORT=8000 go run .)
(cd worker && go run .)
(cd result && go run .)
```

Tests : `go test ./...` dans chaque dossier de service.

## Notes

L'application de vote n'accepte qu'un seul vote par navigateur client. Elle n'enregistre pas de votes supplémentaires si un vote a déjà été soumis par un client.
Il ne s'agit pas d'un exemple d'application distribuée correctement architecturée et parfaitement conçue... c'est juste un
exemple simple des différents types de pièces que vous pourriez voir (files d'attente, données persistantes, etc.), et
comment les gérer dans Docker à un niveau basique.
