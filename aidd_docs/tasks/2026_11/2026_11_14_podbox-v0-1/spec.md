# Podbox — M0 + v0.1 : sandbox réseau fermé utilisable au quotidien

## Target

Un développeur lance Claude Code ou Kiro en mode autonome dans une sandbox Podman rootless, sur son répertoire projet, par une seule commande `podbox`, sans que l'agent puisse joindre un host hors de l'allowlist, lire un fichier sensible de l'hôte ou du projet, ni atteindre le moteur de conteneurs.

## Hard constraints

- Le moteur de conteneurs est Podman rootless ; aucun compte Docker, Docker Desktop ni privilège root n'est requis.
- Distribution sous forme d'un binaire unique `podbox` par OS et architecture (Linux, macOS, Windows ; amd64 et arm64), sans dépendance d'exécution autre que Podman.
- Le conteneur agent n'a aucune route réseau directe vers l'extérieur, vers l'hôte ni vers ses services loopback ; sa seule sortie est le proxy de sa sandbox.
- Le conteneur agent ne peut résoudre aucun nom de domaine externe.
- Egress refusé par défaut : seuls les hosts de l'allowlist (défauts globaux + fichier global + fichier projet) sont joignables ; un deny explicite l'emporte toujours ; une politique projet ne peut pas élargir un deny global.
- Fermeture par défaut : un fichier de politique absent ou invalide n'ouvre aucun accès supplémentaire.
- Le trafic HTTPS autorisé n'est pas déchiffré en v0.1 ; aucune CA n'est installée dans l'agent.
- Une requête bloquée reçoit une réponse lisible par l'agent qui nomme le host refusé et la commande à faire exécuter par l'humain pour l'autoriser.
- L'allowlist d'une sandbox en cours d'exécution se modifie depuis l'hôte sans redémarrer l'agent ; rien dans la sandbox ne peut la modifier.
- Le proxy peut chaîner vers un proxy d'entreprise (y compris avec interception TLS, via une CA d'entreprise fournie), sans que l'agent n'en connaisse ni l'adresse ni la CA.
- Les variables de proxy de l'hôte ne sont jamais transmises au conteneur agent.
- Conteneur agent durci : utilisateur non root, toutes les capabilities retirées, pas d'élévation de privilèges, seccomp par défaut, limites CPU/RAM/PIDs, jamais privilégié, jamais de socket Podman ou Docker monté, aucun montage hôte hors workspace et volume d'auth.
- Mode `shared` : le projet est monté en lecture-écriture ; les chemins sensibles (`.env*`, `*.pem`, `*.key`, `id_*`, `.ssh/`, `.aws/`, `.kube/`, `.npmrc`, `.pypirc`, `.netrc`, `.docker/config.json`, `.git/hooks/`) sont toujours masqués, sans possibilité d'annulation ; `.sandboxignore` (syntaxe gitignore) ajoute des masques.
- L'authentification de l'agent (session Claude Code, session Kiro) persiste entre deux lancements dans un volume dédié par agent, jamais dans le `$HOME` de l'hôte.
- Les images agent (Claude Code, Kiro) se construisent sur le poste à partir de définitions livrées avec `podbox`, sur une image de base configurable (registry interne) ; le proxy ne dépend d'aucun registry.
- Toute commande accepte `--output json` et renvoie un code de sortie stable et documenté.
- Licence open source compatible avec la reprise d'idées des projets cités (MIT ou Apache-2.0).

## Non-goals

- Injection de secrets, interception TLS, CA éphémère, `podbox secret` (v0.2).
- Mode `learn`, logs d'audit structurés (v0.2).
- Mode `ask`, mise en attente des requêtes, MCP `sandbox-control`, skill `operating-in-sandbox` (v0.3).
- Mode `clone`, `diff`, `export`, `cp`, `port`, kits (v0.4).
- Règles par méthode, chemin ou query (exigent le déchiffrement).
- Trafic non HTTP(S) (git SSH, bases de données).
- Runtime microVM, backend Kubernetes, Podman-in-Podman, support de Docker Engine.
- Intégration Sandy (`PodboxProvider`).
- Vérification automatisée sur macOS et Windows : le support est conçu, la CI vérifie Linux uniquement.
- Publication d'images sur un registry public.

## Done-when

- `podbox run claude [path]` et `podbox run kiro [path]` ouvrent une session interactive de l'agent sur le projet, l'agent pouvant joindre son API et lire/écrire le projet.
- Après une première connexion, un second `run` du même agent ne redemande pas d'authentification.
- `podbox ls`, `exec`, `attach`, `stop`, `rm` gèrent les sandboxes créées, en sortie texte et JSON.
- `podbox doctor` signale chaque prérequis manquant (Podman rootless, subuid/subgid, réseau, SELinux) avec une remédiation.
- Depuis la sandbox, échouent toutes : une connexion directe vers Internet, une résolution DNS externe, une connexion vers un service loopback de l'hôte, la lecture de `~/.ssh` de l'hôte, la lecture d'un `.env` du projet, l'accès à un socket Podman ou Docker, une requête vers un host hors allowlist.
- Une requête vers un host hors allowlist renvoie le message lisible par l'agent ; après `podbox policy allow <sandbox> <host>`, la même requête réussit sans redémarrage.
- Derrière un proxy d'entreprise obligatoire, l'agent joint un host autorisé.
- Les tests d'évasion ci-dessus tournent en CI Linux sur chaque changement et passent.

## Open Questions

None

## Stakeholders

- Decider: Guillaume (auteur)
- Owner: Crocogiciel Studio (repo `Crocogiciel-Studio/Podbox`)
- Consumer: développeurs en entreprise sans Docker ; Sandy (intégration ultérieure)

## Context

- Étude de faisabilité complète : [feasibility-study.md](./feasibility-study.md).
- Roadmap révisée et arbitrages (allowlist sans déchiffrement, agent sans DNS, proxy embarqué, chaînage d'entreprise avancé en v0.1) : décidés en session d'onboarding le 2026-11-14.
- Constat M0 préliminaire (Podman 5.7, rootless, Linux) : Podman transmet par défaut les variables de proxy de l'hôte aux conteneurs, et un réseau sans DNS hérite quand même du `resolv.conf` de l'hôte.
