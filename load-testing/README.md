# Test de charge

`vote.js` est un script [k6](https://grafana.com/docs/k6/latest/) qui simule des votants : chaque utilisateur virtuel (_VU_) affiche la page de vote, vote au hasard, puis attend une seconde avant de recommencer. Le nombre d'utilisateurs monte à 50 en 30 secondes, reste à 50 pendant 2 minutes, puis redescend à 0.

Lancez-le depuis la racine du dépôt avec l'image Docker de k6, sans rien installer, en remplaçant `<url>` par l'URL de l'application (sans `/` final) :

```sh
cat load-testing/vote.js | docker run --rm -i -e BASE_URL=<url> grafana/k6 run -
```

La même commande fonctionne sous Linux, macOS et Windows (PowerShell).

Le script vise `/vote/`, la page de vote du monolith. Pour viser le service `vote` des microservices, qui sert sa page à la racine, ajoutez `-e VOTE_PATH=/`.

En local, avec `make dev monolith`, l'URL est `http://host.docker.internal:5002` (Docker Desktop) ou, sous Linux, ajoutez `--network host` et utilisez `http://localhost:5002`.

Ne visez que des applications que vous avez déployées vous-même.
