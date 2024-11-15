# Application de vote

Une application distribuée simple qui permet de voter !

## Architecture

![Diagramme d'architecture](architecture.excalidraw.png)

* Un front-end en Python qui permet le vote entre deux options
* Un Redis qui collecte les nouveaux votes
* Un worker en .NET qui consomme les votes
* Un base de données Postgres qui stocke les résultats
* Une web app en Node.js qui affiche les résultats en temps réel

## Notes

L'application de vote n'accepte qu'un seul vote par navigateur client. Elle n'enregistre pas de votes supplémentaires si un vote a déjà été soumis par un client.
Il ne s'agit pas d'un exemple d'application distribuée correctement architecturée et parfaitement conçue... c'est juste un simple
exemple simple des différents types de pièces et de langages que vous pourriez voir (files d'attente, données persistantes, etc.), et comment les gérer dans Docker au niveau de l'architecture.
comment les gérer dans Docker à un niveau basique.

Traduit avec DeepL.com (version gratuite)
