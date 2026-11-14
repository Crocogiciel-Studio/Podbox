# Podman Agent Sandbox — spec & étude de faisabilité

Nov 14, 2026 · @Guillaume

## Contexte et objectifs

Objectif : une CLI open source qui reproduit l'expérience Docker Sandboxes (sbx) sur **Podman rootless**, pour faire tourner des coding agents en mode autonome (YOLO) sans risque pour le poste hôte. Elle doit pouvoir être utilisée seule, puis intégrée plus tard dans Sandy (l'ADE open source de l'auteur, aujourd'hui basé sur sbx), qui doit continuer à fonctionner avec les deux.

Nom de travail utilisé dans ce document : `podsbox` (à remplacer).

### Contraintes de l'environnement cible (entreprise)

- Pas de compte Docker utilisable, donc pas de sbx ni de Docker Desktop.
- Stack : Python, Java, TypeScript ; conteneurs **Podman** ; déploiements **Kubernetes** (possiblement OpenShift, à vérifier).
- Coding agent principal de l'équipe : **Kiro** (IDE + CLI). Claude Code utilisé aussi.
- Registry interne probable (Artifactory/Nexus) ; Docker Hub possiblement bloqué. Proxy d'entreprise possiblement en interception TLS.
- Poste de travail : OS à confirmer (Linux, macOS ou Windows/WSL2).

### Features demandées

1. Proxy qui injecte les secrets (l'agent ne voit jamais les vraies clés).
2. Politique réseau (allowlist, deny par défaut), avec **approbation interactive intégrée au flux de l'agent**.
3. Montage du répertoire projet en mode **partagé** ou **isolé** (équivalent `--clone` de sbx), avec **diff et export** du travail de l'agent.
4. Fichier d'exclusion type `.sandboxignore` pour ne jamais exposer `.env`, clés, etc.
5. Kits : connecter des MCP, installer des skills, composer des environnements (comme les kits sbx).

### Non-objectifs (v1)

- Pas d'isolation microVM au départ (backend optionnel plus tard).
- Pas de backend Kubernetes au départ.
- Pas de gestion OAuth par le proxy au départ (clés API uniquement).

## Architecture cible

Deux repos : la **CLI de sandboxing** (ce projet, open source, autonome) et **Sandy**, qui la consomme via une interface de provider. La logique « ADE » (diff, revue, UI d'approbation) vit dans Sandy ; la CLI reste un moteur.

&#91;embedded content: architecture d'une sandbox · agent, proxy, CLI hôte\]

L'agent n'a qu'une sortie : le proxy, seul conteneur sur les deux réseaux. Les décisions passent par l'API de contrôle, joignable uniquement depuis l'hôte.

### Composants par sandbox

- **Conteneur agent** : rootless, sur un réseau Podman `--internal` (aucune route vers l'extérieur), workspace monté, aucun secret réel.
- **Conteneur proxy** (un par sandbox) : seul conteneur sur deux réseaux (interne + egress). Data plane = allowlist, injection de secrets, logs. Control plane = API locale accessible **uniquement depuis l'hôte** (socket unix).
- **Résolveur DNS** filtré (sinkhole des domaines non autorisés), intégré au proxy ou en sidecar.
- **MCP `sandbox-control`** dans le conteneur agent : permet à l'agent de lire la politique et de demander un accès (jamais d'approuver).
- **CLI hôte** : cycle de vie, secrets (keychain OS), politique, flux d'événements JSON pour Sandy.

### Choix techniques

- **Langage : Go.** Bindings Podman, client-go pour un futur backend K8s, binaire unique, bonnes libs de proxy (goproxy, martian). Utilisable aussi comme librairie par Sandy.
- **Runtime pluggable dès le départ** (interface `Runtime`) : `podman` (v1), `podman --runtime krun` / Kata (microVM, plus tard), `k8s` (plus tard). L'écart d'isolation avec sbx devient une option, pas une limite du produit.
- **API Podman** via le socket (compatible Docker) : permet potentiellement de supporter Docker Engine rootless à moindre coût.
- **Surface CLI calquée sur sbx** (`run`, `exec`, `cp`, `ls`, `rm`, `policy`, `secret`, `kit`) pour que l'adapter Sandy soit quasi identique entre les deux providers.
- **`--output json` sur toutes les commandes** dès la v0.1.

### Contrat avec Sandy (à figer tôt)

```
SandboxProvider
  create / start / stop / rm / ls
  exec / attach (PTY)
  cp in/out, ports, logs
  policy (get / allow / deny / watch events), secrets
  (diff/export : implémentés côté Sandy via exec + cp)
├── SbxProvider     → sbx CLI
└── PodsboxProvider → cette CLI (ou sa lib Go)
```

Recommandation : écrire l'interface et le `SbxProvider` en premier dans Sandy ; la CLI n'a plus qu'à remplir le même contrat.

## Modèle d'isolation

Podman rootless isole l'agent entier avec ses propres namespaces, mais partage le noyau de l'hôte : c'est moins fort qu'une microVM sbx, et très au-dessus d'un agent lancé directement sur le poste. Les sandboxes natives des agents (Kiro `/sandbox`, Claude Code `/sandbox`) ne sont que des garde-fous : elles restreignent les commandes lancées par l'agent, pas l'agent lui-même.

| Niveau | Noyau | Agent entier isolé | Verdict |
| --- | --- | --- | --- |
| Sandbox native Kiro / Claude Code (bubblewrap, Seatbelt, AppContainer) | hôte | non, seulement ses commandes ; les outils fichiers intégrés restent hors sandbox | garde-fou complémentaire |
| Podman rootless (v1) | partagé | oui | bon |
| Pod K8s durci | partagé avec le nœud | oui | bon, si bien configuré |
| Pod gVisor | noyau en espace utilisateur | oui | très bon |
| Podman `--runtime krun` / Kata | microVM, noyau propre | oui | équivalent sbx |

### Durcissement par défaut du conteneur agent

- Rootless, `--userns=keep-id`, utilisateur non root dans l'image.
- `--cap-drop=ALL`, `--security-opt no-new-privileges`, seccomp par défaut.
- Jamais `--privileged` à part de rares cas en rootless. Jamais de socket Podman ou Docker monté.
- Aucun montage hôte hors workspace et volumes d'état de l'agent. Refuser les dot-dirs du `$HOME` (`.ssh`, `.aws`, `.kube`, `.config`…).
- Limites CPU, RAM, PIDs.
- Réseau `--internal` : aucune route directe vers Internet ni vers les services loopback de l'hôte.
- Rootfs en lecture seule quand c'est possible, avec tmpfs pour `/tmp`.

### Pourquoi jamais de conteneur privilégié rootful

`--privileged` donne toutes les capabilities, désactive seccomp et AppArmor/SELinux et expose les devices de l'hôte : en rootful, c'est équivalent à root sur la machine (montage du disque hôte, modules noyau). En K8s, s'y ajoutent les tokens de service account, les credentials du kubelet et les secrets des autres pods du nœud. En **rootless**, le user namespace limite `--privileged` aux droits de l'utilisateur, ce qui rend possible Podman-in-Podman (Testcontainers) sans ouvrir l'hôte.

### Backend Kubernetes (plus tard)

- Pod durci : `automountServiceAccountToken: false`, `runAsNonRoot`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, limites de ressources, namespace dédié sans secrets, pas de `hostPath`.
- NetworkPolicy deny-all en egress, sauf vers le **pod proxy** et le DNS. Attention : une NetworkPolicy s'applique au pod entier, donc **un sidecar proxy dans le même pod ne peut pas être le seul à sortir**. Proxy = pod séparé (recommandé), ou sidecar avec redirection iptables par UID (exige `NET_ADMIN` dans un init container).
- Bloquer l'endpoint de métadonnées cloud (`169.254.169.254`).
- Isolation renforcée : `RuntimeClass` gVisor ou Kata si la plateforme les fournit.
- Pour donner un cluster à l'agent : **vcluster** (cluster virtuel dans un namespace, sans privilèges), plutôt que kind/k3d qui exigent un pod privilégié.

## Proxy d'egress et injection de secrets

Même modèle que sbx : un proxy HTTPS avec interception TLS, dont la CA est cuite dans l'image de l'agent, remplace des valeurs sentinelles par les vrais secrets, uniquement pour les hosts autorisés. L'agent ne voit que des placeholders.

### Règles non négociables

1. L'agent n'a **aucune route directe** vers l'extérieur (réseau `--internal`). Sans ça, le proxy est contournable.
2. Les secrets réels n'existent **que dans le proxy** (keychain OS côté hôte → Podman secret monté dans le seul conteneur proxy).
3. Injection **par host de destination** : une clé GitLab ne part que vers GitLab.
4. Le control plane (approbation, modification de politique) n'est **jamais joignable depuis la sandbox**.

### Deux modes d'injection

| Mode | Fonctionnement | Avantages | Contraintes |
| --- | --- | --- | --- |
| A. Forward proxy avec interception TLS | `HTTPS_PROXY=http://proxy:8080` ; le proxy présente un certificat signé par sa CA, déchiffre, injecte, rechiffre | marche pour tous les outils sans reconfiguration | la CA doit être acceptée par tous les trust stores ; impossible avec le certificate pinning |
| B. Reverse proxy | l'outil pointe sur `http://proxy:8080/<service>` (ex. `ANTHROPIC_BASE_URL`, mirror Maven, `index-url` pip) ; le proxy ouvre la vraie connexion HTTPS | pas de CA à distribuer | seulement pour les outils dont l'URL de base est configurable |

En pratique : B pour les registries de paquets et les API LLM, A pour le reste.

### Pourquoi la CA est le point délicat

Pour ajouter un header dans une requête HTTPS, le proxy doit la déchiffrer, donc se faire passer pour le serveur avec un certificat signé par sa propre CA. Chaque écosystème a son trust store :

| Outil | Trust store | Réglage |
| --- | --- | --- |
| curl, git, la plupart des binaires | système | `update-ca-certificates` / `update-ca-trust` |
| Python (requests, pip) | certifi | `REQUESTS_CA_BUNDLE`, `SSL_CERT_FILE` |
| Node, npm | interne | `NODE_EXTRA_CA_CERTS` |
| Java, Maven, Gradle | `cacerts` de la JVM | `keytool -importcert -cacerts` (sur RHEL/UBI, `update-ca-trust` le met souvent à jour) |
| Java (proxy) | ignore `HTTPS_PROXY` | `JAVA_TOOL_OPTIONS=-Dhttps.proxyHost=… -Dhttps.proxyPort=…` |
| Conteneurs lancés dans la sandbox | leurs propres trust stores | installer la CA dans les images de build |

C'est un coût payé une fois dans l'image de base (section Images, kits). Recommandation : **une CA éphémère par sandbox** (comme Modal, Cloudflare, Lens), ou au minimum une CA dédiée dont la clé privée reste dans le seul proxy.

### Implantation de référence (prototype Podman)

```bash
podman network create --internal sbx-internal
podman network create sbx-egress
printf '%s' "$ANTHROPIC_KEY" | podman secret create anthropic_key -

podman run -d --name proxy --network sbx-internal \
  --secret anthropic_key,type=env,target=ANTHROPIC_API_KEY \
  -v ./mitmproxy-ca.pem:/home/mitmproxy/.mitmproxy/mitmproxy-ca.pem:ro,Z \
  -v ./addon.py:/addon.py:ro,Z \
  docker.io/mitmproxy/mitmproxy mitmdump -s /addon.py --listen-port 8080
podman network connect sbx-egress proxy

podman run -it --rm --network sbx-internal --userns=keep-id \
  -e ANTHROPIC_API_KEY=placeholder \
  -v "$(pwd)":/workspace:Z -w /workspace \
  sbx/claude:polyglot
```

Addon mitmproxy minimal (prototype ; la version finale peut être réécrite en Go) :

```python
import os
from mitmproxy import http

INJECT = {"api.anthropic.com": ("x-api-key", os.environ["ANTHROPIC_API_KEY"])}
ALLOW = set(INJECT) | {"pypi.org", "files.pythonhosted.org"}

def http_connect(flow: http.HTTPFlow):
    if flow.request.pretty_host not in ALLOW:  # bloque avant le TLS
        flow.response = http.Response.make(403, b"blocked by sandbox proxy")

def request(flow: http.HTTPFlow):
    host = flow.request.pretty_host
    if host in INJECT:
        header, value = INJECT[host]
        flow.request.headers[header] = value
```

### Améliorations à prévoir

- **Rediriger les réponses** : remplacer tout secret réel renvoyé par l'upstream par son placeholder (fait par agentcage).
- **Placeholders aléatoires et rotatifs** plutôt qu'une chaîne fixe.
- **Chaînage vers le proxy d'entreprise** (`--mode upstream:…`) : c'est le conteneur proxy qui fait confiance à la CA d'entreprise, l'agent ne connaît que la CA de la sandbox.
- **Git** : HTTPS avec token injecté (le SSH ne passe pas par un proxy HTTP).
- **OAuth** (Claude Code avec abonnement, Kiro en SSO) : tokens rafraîchis, donc interception du flux de refresh agent par agent. Hors v1 : le token OAuth vit dans un volume dédié de la sandbox, avec des droits et une durée de vie minimaux.

## Politique réseau et approbation interactive

Différenciateur clé face à sbx : l'agent sait qu'il est dans une sandbox avec une politique réseau, et demande un accès **dans son propre flux** ; un humain décide, jamais l'agent.

### Modèle de politique

Couches du général au particulier, un deny l'emporte toujours :

`défauts globaux → kit → projet (.sandy/policy.yaml, versionné) → grants de session`

```yaml
mode: ask            # strict | ask | learn
default: deny
rules:
  - host: "*.pythonhosted.org"
    allow: true
  - host: "api.anthropic.com"
    allow: true
    inject: { header: x-api-key, secret: anthropic }
  - host: "gitlab.example.internal"
    allow: true
    methods: [GET, POST]          # règles fines optionnelles : méthode, chemin, query
    paths: ["/api/v4/projects/123/"]
  - host: "*.pastebin.com"
    allow: false     # deny explicite : jamais de demande
approval:
  hold_timeout: 30s
  scopes: [once, session, project]   # "project" réécrit policy.yaml
```

| Mode | Comportement | Usage |
| --- | --- | --- |
| `strict` | tout ce qui n'est pas autorisé est refusé, sans question | CI, tâches autonomes |
| `ask` | requête inconnue mise en attente, événement émis, décision humaine | travail interactif (Sandy, TUI) |
| `learn` | tout passe et tout est logué ; en fin de session, la CLI propose un `policy.yaml` généré à partir du trafic | onboarding d'un repo, puis passage en `strict` |

### Flux d'approbation

1. **Réactif** : une commande joint un host inconnu. En mode `ask`, le proxy met la requête en attente (`hold_timeout` borné, car pip, Maven, etc. ont des timeouts et retentent) et émet un événement. Refus ou timeout : 403 avec un corps **lisible par l'agent** :

   ```
   Blocked by sandbox network policy: host "foo.com" is not allowed.
   To request access, call the MCP tool `request_network_access` with the host and your reason.
   ```
2. **Proactif** : le MCP `sandbox-control` expose `get_network_policy()` et `request_network_access(host, reason)`. L'appel bloque jusqu'à la décision, puis renvoie `allowed` ou `denied`. La demande apparaît dans le fil de l'agent (Claude, Kiro) avec sa justification.
3. Une **skill / steering** de base explique à l'agent le modèle (proxy, où lire la politique, quoi faire quand c'est bloqué). Elle est installée par le kit de base pour chaque agent.
4. Le MCP joint `policy.sandbox.local`, host virtuel servi par le proxy, qui n'accepte que les **demandes**. Les décisions passent par le control API hôte (socket unix).

### Sécurité de l'approbation

- L'agent et sa justification ne sont pas fiables (prompt injection). L'UI met en avant les **faits** : host, port, méthode, chemin, taille du body ; la raison de l'agent en second plan. Le proxy peut déchiffrer la requête en attente pour afficher `POST /api/upload (2,3 Mo)` plutôt que `foo.com`.
- La sandbox peut demander, **jamais approuver**.
- Contre la fatigue d'approbation : limite de débit, regroupement des demandes, deny explicites silencieux.
- Le proxy sait quelle sandbox a émis la requête, pas quelle commande. Seul l'agent peut le préciser via le MCP, et c'est déclaratif.
- Différence avec agentcage : chez eux, un agent LLM « Decider » peut accorder l'accès sans humain. Ici, **humain dans la boucle par défaut**.

### Au-delà du HTTP

- **DNS** : un réseau `--internal` peut encore résoudre des noms externes via aardvark-dns, ce qui ouvre un canal d'exfiltration. Fournir un résolveur filtré (dnsmasq avec sinkhole des domaines non autorisés) et le tester.
- **Non-HTTP** (git en SSH, bases de données) : bloqué par défaut ; forwards TCP explicites dans la politique pour les besoins réels.
- Bloquer les IP privées et les endpoints de métadonnées, avec une vérification anti DNS-rebinding.
- L'allowlist limite les **destinations**, pas le **contenu** : un host autorisé reste un canal d'exfiltration possible. Les logs d'audit du proxy sont le filet de sécurité.

### Événements du control API (format à figer)

```json
{"type":"network.request.pending","sandbox":"sbx-42","id":"req-7f3",
 "host":"adoptium.net","port":443,"method":"GET","path":"/download/jdk21.tar.gz",
 "bodyBytes":0,"agentReason":"Télécharger le JDK pour builder","source":"mcp|proxy",
 "expiresAt":"2026-11-14T19:30:00Z"}
```

Commandes associées : `podsbox policy watch --json` (flux), `podsbox policy allow <sbx> <host> --scope once|session|project`, `podsbox policy deny …`, `podsbox policy log`.

## Workspace : partagé, clone, exclusions, diff

Le mode clone est le seul qui garantit que rien ne bouge sur le disque hôte sans accord : le travail de l'agent devient une **proposition** que l'on relit, puis que l'on accepte en tout ou en partie.

| Mode | Montage | Exclusions | Barrière de revue | Limites |
| --- | --- | --- | --- | --- |
| `shared` | bind mount du repo, `--userns=keep-id`, `:Z` sous SELinux | par masquage (voir plus bas) | non : les écritures touchent l'hôte | modifications mêlées aux vôtres ; lent sur macOS (VM) |
| `clone` | copie du working tree (y compris non commité) dans un volume nommé ou un snapshot copy-on-write | triviales à la copie | oui | taille du repo, copie initiale |

### `.sandboxignore`

- Syntaxe gitignore. Patterns sensibles **toujours exclus et non annulables** : `.env*`, `*.pem`, `*.key`, `id_*`, `.ssh/`, `.aws/`, `.kube/`, `.npmrc`, `.pypirc`, `.netrc`, `.docker/config.json` (à compléter). Inspiration : `.broodboxignore` de brood-box.
- Warning au lancement si un fichier qui ressemble à un secret n'est pas couvert (heuristique : nom + entropie).
- **Mode clone** : exclusions appliquées à la copie (logique `rsync --exclude-from`).
- **Mode partagé** : un bind mount ne sait pas exclure. On monte le repo, puis on **masque** chaque chemin ignoré par un fichier vide (`/dev/null`) ou un tmpfs monté par-dessus. Limites : globs résolus au démarrage seulement ; impossible pour des milliers de chemins (réserver aux secrets) ; ce que l'agent écrit dans un chemin masqué part dans le tmpfs et disparaît.
- Masquer aussi par défaut `.git/hooks/` (exécution de code côté hôte) ; c'est ce que fait agentcage.

### Diff du travail de l'agent (mode clone)

1. À la création : copie du working tree, puis **commit de base** (snapshot exact) sur une branche `sandy/<id>` ; on enregistre son SHA.
2. Le diff = commit de base comparé à l'état courant, que l'agent ait commité ou non. Pour inclure les fichiers non suivis sans toucher à l'index de l'agent :

   ```bash
   GIT_INDEX_FILE=/tmp/sandy.idx git add -A
   GIT_INDEX_FILE=/tmp/sandy.idx git diff --cached --stat <base>
   ```
3. Export au choix :
   - **Branche** : `git bundle create` dans la sandbox, `cp` vers l'hôte, puis `git fetch <bundle> sandy/<id>`.
   - **Patch** : `git apply --3way` sur le working tree hôte.
   - **PR** : push vers le remote via le proxy (token injecté).
4. Le SHA de base permet un merge 3-way ou un rebase propre si l'hôte a avancé entre-temps.
5. Alternative inspirée de brood-box : snapshot copy-on-write (FICLONE sur Linux, `clonefile` sur macOS), diff fichier par fichier, écriture des fichiers acceptés après revérification des hashs, bits setuid/setgid supprimés.

En mode partagé, un diff reste possible : `git stash create` au lancement produit un snapshot sans rien modifier. Mais il n'y a pas de barrière de revue.

### Ce que ça permet côté Sandy (plus tard)

- Diff en direct pendant que l'agent travaille ; acceptation par fichier ou par hunk.
- **Checkpoints** automatiques (à chaque tour d'agent via un hook, ou toutes les N minutes) et retour en arrière.
- Plusieurs sandboxes sur la même base (Kiro contre Claude, deux prompts) et comparaison des diffs.

**Point de design** : la logique de diff vit **dans Sandy**, pas dans la CLI. Elle n'a besoin que de `exec` et `cp`, que sbx et la CLI fournissent toutes les deux, donc elle marche avec les deux providers. La CLI peut exposer un raccourci `podsbox diff <id> --json` pour un usage sans Sandy.

## Images, kits, MCP et skills

Une image par agent, construite en couches pour ne maintenir chaque chose qu'à un endroit. Un kit = une image (ou une couche) + une politique réseau + une config runtime (MCP, skills, env, volumes d'auth).

### Couches d'images

```
sbx-base          ← CA de la sandbox, variables proxy, user 1000, git, outils de base
   └── sbx-polyglot   ← JDK + Maven/Gradle, Python + uv, Node + pnpm
         ├── sbx-kiro     ← kiro-cli + config
         ├── sbx-claude   ← Claude Code + config
         └── sbx-codex    ← …
```

- La base change rarement ; la toolchain est lourde et partagée ; la couche agent est petite et change chaque semaine.
- L'agent prend sa base en paramètre (`ARG BASE`), ce qui permet des variantes de toolchain (`sbx/kiro:java`, `sbx/kiro:polyglot`) sans toucher à l'agent.

Image de base (extrait) :

```dockerfile
FROM registry.example.internal/ubi9/openjdk-21
USER root
COPY sbx-ca.crt /etc/pki/ca-trust/source/anchors/sbx-ca.crt
RUN update-ca-trust \
 && keytool -importcert -noprompt -cacerts -storepass changeit \
      -alias sbx-ca -file /etc/pki/ca-trust/source/anchors/sbx-ca.crt || true
ENV HTTPS_PROXY=http://proxy:8080 HTTP_PROXY=http://proxy:8080 \
    NO_PROXY=localhost,127.0.0.1 \
    SSL_CERT_FILE=/etc/pki/tls/certs/ca-bundle.crt \
    REQUESTS_CA_BUNDLE=/etc/pki/tls/certs/ca-bundle.crt \
    NODE_EXTRA_CA_CERTS=/etc/pki/ca-trust/source/anchors/sbx-ca.crt \
    JAVA_TOOL_OPTIONS="-Dhttps.proxyHost=proxy -Dhttps.proxyPort=8080 -Dhttp.proxyHost=proxy -Dhttp.proxyPort=8080 -Dhttp.nonProxyHosts=localhost"
USER 1000
```

Couche agent (exemple Kiro) :

```dockerfile
ARG BASE=registry.example.internal/sbx/polyglot:2026.11
FROM ${BASE}
ARG KIRO_VERSION=2.31.0
USER root
RUN curl -fsSL https://cli.kiro.dev/install | bash   # idéalement : version pinnée, binaire depuis le registry interne
USER 1000
LABEL sbx.agent="kiro" sbx.agent.version="${KIRO_VERSION}"
ENTRYPOINT ["kiro-cli", "chat", "--v3"]
```

Si la CA est éphémère par sandbox, elle est injectée au démarrage (volume en lecture seule + script d'entrée qui met à jour les trust stores) au lieu d'être cuite dans l'image.

### Format de kit (neutre, à spécifier)

```yaml
name: kiro-java
image: { base: sbx/polyglot, agent: kiro }   # ou une couche de paquets/fichiers
env: { KIRO_LOG: info }
network: { extends: base, rules: [ { host: "*.kiro.dev", allow: true } ] }
secrets: [ { name: gitlab, host: gitlab.example.internal, header: PRIVATE-TOKEN } ]
mcp:
  - { name: sandbox-control, transport: stdio, builtin: true }
  - { name: jira, transport: http, url: https://mcp.example.internal/jira, secret: jira }
skills: [ { name: operating-in-sandbox, builtin: true }, { path: ./skills/java-conventions } ]
volumes: { auth: ~/.local/share/kiro-cli }
```

- **Compilation en deux parties** : une couche d'image mise en cache par hash (démarrage rapide) et une config runtime appliquée au lancement.
- **Adapters par agent** : le kit décrit les MCP et skills une fois ; chaque adapter génère le bon format (`.mcp.json` / `~/.claude.json` et `.claude/skills` pour Claude Code ; `~/.kiro/settings/mcp.json`, steering et powers pour Kiro ; `config.toml` pour Codex). C'est le poste le plus coûteux à maintenir, car ces formats changent souvent.
- **Question ouverte** : lire directement le format de kit sbx si celui-ci est documenté et stable (compatibilité gratuite « un kit pour les deux outils »), sinon traduire le format neutre vers sbx.

### Trois cas de MCP

| Cas | Fonctionnement | Secrets |
| --- | --- | --- |
| stdio, dans la sandbox | installé par le kit dans l'image ou au lancement | via le proxy pour ses appels HTTP sortants |
| HTTP distant | passe par le proxy, host dans l'allowlist | **token injecté par le proxy** : l'agent ne le voit pas |
| sur l'hôte | pont vers `host.containers.internal`, autorisé explicitement | restent sur l'hôte |

Alternative à étudier (kuza/agentbox) : un **sidecar MCP** par service, qui détient le token et expose un endpoint MCP sans secret à l'agent.

### Maintenance

- Repo dédié : `base/`, `toolchains/`, `agents/`, `kits/`, `policies/`.
- CI : rebuild hebdomadaire (CVE, mises à jour d'agents), push sur le registry (interne en entreprise, GHCR en open source), tags datés + `latest`, versions pinnées, scan d'images (Trivy ou équivalent).

## Spécification CLI

Verbes calqués sur sbx ; chaque commande accepte `--output json` et renvoie un code de sortie stable. Proposition de première surface, à valider :

| Commande | Rôle | Version |
| --- | --- | --- |
| `podsbox run <agent\|kit> [path] [--mode shared\|clone] [--name] [--policy strict\|ask\|learn]` | crée et attache une sandbox (agent interactif) | v0.1 |
| `podsbox create` / `start` / `stop` / `rm` / `ls` | cycle de vie, sandboxes persistantes | v0.1 |
| `podsbox exec <id> -- <cmd>` / `attach <id>` | commande ou PTY dans la sandbox | v0.1 |
| `podsbox doctor` | vérifie Podman rootless, subuid/subgid, réseaux, DNS, SELinux | v0.1 |
| `podsbox policy show\|allow\|deny\|log\|watch\|reload` | politique réseau, approbations, audit | v0.1 (allowlist), v0.4 (approbations) |
| `podsbox secret set\|ls\|rm` | secrets dans le keychain OS | v0.2 |
| `podsbox cp <src> <dst>` | copie de fichiers (screenshots, artefacts) | v0.3 |
| `podsbox port <id> <port>` | exposition d'un port de la sandbox sur `127.0.0.1` | v0.3 |
| `podsbox diff <id>` / `export <id> --as branch\|patch\|push` | travail de l'agent en mode clone | v0.3 |
| `podsbox kit ls\|add\|build` | kits : images, MCP, skills, politique | v0.3 / v0.4 |

### Fichiers de configuration

- Global : `~/.config/podsbox/config.yaml`, `~/.config/podsbox/policy.yaml`.
- Projet : `.sandy/policy.yaml` (versionné), `.sandboxignore`, `.sandy/kit.yaml` optionnel.
- État : `~/.local/share/podsbox/<id>/` (métadonnées, SHA de base, logs d'audit).

### Exigences transverses

- Fermeture par défaut : un fichier absent ou invalide = aucun accès supplémentaire, jamais l'inverse.
- Une politique de projet ne peut **pas élargir** la politique globale (règle de brood-box).
- Toute action du proxy est loguée (décision, host, méthode, chemin, taille) dans un format exploitable par Sandy.
- Tests d'évasion automatisés dans la CI : lecture de `~/.ssh` hôte, sortie réseau directe, DNS externe, accès au socket Podman, exfiltration d'un faux secret (inspiration : script de test de thenaithan/agent-sandbox-podman).

## Roadmap MVP

Quatre versions séquentielles, puis des chantiers ultérieurs ; chacune est utilisable seule et se termine par un critère de sortie vérifiable.

### v0.1 — Sandbox réseau fermé

- [ ] Squelette Go, interface `Runtime`, implémentation Podman rootless via le socket
- [ ] `run` / `exec` / `attach` / `ls` / `rm` / `doctor`, `--output json` partout
- [ ] Durcissement par défaut du conteneur agent (section Isolation)
- [ ] Réseau `--internal` + conteneur proxy en allowlist (sans injection) + DNS filtré
- [ ] Mode `shared` + `.sandboxignore` par masquage, avec patterns sensibles forcés
- [ ] Images `sbx-base`, `sbx-polyglot`, `sbx-claude`, `sbx-kiro`

Critère de sortie : Claude Code et Kiro tournent en mode autonome ; les tests d'évasion (réseau direct, DNS externe, `~/.ssh` hôte, socket Podman) échouent tous.

### v0.2 — Secrets

- [ ] `secret set|ls|rm` (keychain OS, fallback fichier chiffré)
- [ ] Injection par host avec placeholders aléatoires ; rédaction des secrets dans les réponses
- [ ] CA éphémère par sandbox, distribuée à tous les trust stores (système, Python, Node, Java)
- [ ] Logs d'audit du proxy

Critère de sortie : `printenv` et la lecture du disque dans la sandbox ne révèlent aucun secret réel ; les appels API fonctionnent.

### v0.3 — Clone, diff, kits v1

- [ ] Mode `clone` (copie avec exclusions, commit de base, branche `sandy/<id>`)
- [ ] `diff` / `export --as branch|patch|push`
- [ ] `cp`, `port`
- [ ] Kits v1 : image + env + politique

Critère de sortie : le travail d'un agent revient sous forme de branche relisible, sans qu'aucun fichier hôte ait changé avant l'export.

### v0.4 — Approbations et agents

- [ ] Mode `ask` : mise en attente, événements `policy watch --json`, `policy allow|deny --scope`
- [ ] MCP `sandbox-control` + skill `operating-in-sandbox` pour chaque agent
- [ ] Mode `learn` + génération de `policy.yaml`
- [ ] Adapters MCP et skills pour Claude Code et Kiro
- [ ] TUI minimale pour les approbations sans Sandy

Critère de sortie : un agent bloqué demande l'accès via le MCP, un humain approuve, l'agent reprend sans intervention manuelle.

### Plus tard

- Runtime microVM (`podman --runtime krun`, Kata ; regarder `go-microvm` de brood-box)
- Backend Kubernetes (pod durci, proxy en pod séparé, vcluster optionnel)
- OAuth géré par le proxy, agent par agent
- Podman-in-Podman rootless (Testcontainers)
- Intégration Sandy (`PodsboxProvider`)

## Prior art et inspirations

Aucun projet open source ne couvre toute la liste, mais chaque feature existe déjà quelque part, souvent en Go et sous licence MIT ou Apache-2.0. Stratégie retenue : une CLI propre en Go, qui reprend les meilleures idées ; lancer agentcage et mattolson/agent-sandbox en local avant de figer l'architecture. Vérifier les licences avant toute reprise de code et conserver les mentions de copyright.

| Projet | Techno / licence | Ce qu'on reprend | Ce qui manque |
| --- | --- | --- | --- |
| [agentcage](https://github.com/agentcage/agentcage) | probablement Python, MIT, \~22 étoiles | Podman rootless + quadlets, egress deny par défaut, placeholders aléatoires et rédaction des réponses, mitmdump + chaîne d'inspecteurs, DNS sinkhole (dnsmasq), blocage IP privées et métadonnées, **Policy API** `agentcage.local` où l'agent demande un accès avec justification, masques tmpfs sur `.git/hooks/` | MCP, skills, kits, mode clone ; décision par un LLM « Decider » et non un humain ; petit projet sans release |
| [mattolson/agent-sandbox](https://github.com/mattolson/agent-sandbox) | Go + addon mitmproxy (`enforcer.py`), MIT, \~212 étoiles | politique YAML en couches (utilisateur, par agent) avec règles par scheme / méthode / chemin / query, rechargement à chaud, secrets montés dans le seul proxy, firewall iptables, skill `operating-in-agent-sandbox` et politique lisible dans `/run/agentbox/policy.yaml` | pensé pour Docker / compose, pas de MCP, early stage |
| [brood-box (Stacklok)](https://awesome.ecosyste.ms/projects/github.com%2Fstacklok%2Fbrood-box) | Go pur, Apache-2.0, \~20 étoiles | microVM libkrun, snapshot copy-on-write du workspace, revue du diff fichier par fichier, `.broodboxignore` avec patterns sensibles forcés, profils d'egress, une config projet ne peut pas élargir le global | pas de proxy d'injection (secrets passés en env dans la VM), exige KVM, expérimental |
| [container-use (Dagger)](https://docsearch.algolia.com/mcp/docs/repo/dagger/container-use) | Go, \~3,6k étoiles | un conteneur + une branche git par agent, historique en git notes, exposition en MCP, revue par `git checkout` | dépend de Dagger / Docker, pas de proxy de secrets |
| [kuza/agentbox](https://github.com/kuza/agentbox) | Podman rootless | réseau `--internal`, Squid en allowlist, **sidecars MCP** qui détiennent les tokens hors de la box | pas d'injection, Claude Code et pi uniquement |
| [devgugga/agent-sandbox](https://github.com/devgugga/agent-sandbox) | Podman rootless + systemd | clone par workspace, `.agent-sandbox.toml` par projet (domaines, ports, Podman imbriqué, services Postgres / Redis), `reload-allowlist` sans redémarrer l'agent | pas d'injection de secrets |
| [thomaspeklak/agent-sandbox (ags)](https://github.com/thomaspeklak/agent-sandbox) | Rust, Podman rootless | multi-agents (pi, claude, codex, gemini, opencode), sources de secrets ordonnées (env, secret store, commande hôte), prompts allow / deny via zenity / kdialog | jeune ; approbations en popup de bureau |
| [h5i](https://medium.com/@Koukyosyumei/h5i-a-lightweight-rootless-container-sandbox-for-claude-code-and-codex-e9a9eb0cf71e) | Podman rootless | capabilities toutes supprimées, rootfs en lecture seule, workspace git jetable, proxy de credentials côté hôte, patch + traces d'exécution pour la revue | peu documenté |
| [thenaithan/agent-sandbox-podman](https://github.com/thenaithan/agent-sandbox-podman) | Podman rootless | **script qui teste les limites** sur la machine (montages, sockets, loopback hôte via pasta `--no-map-gw`) | egress ouvert |
| [ftnilsson/dev-containers](https://github.com/ftnilsson/dev-containers) | Podman + devcontainer | Squid sidecar en CONNECT sans déchiffrement (pas de CA) : bon modèle pour un mode « allowlist sans injection » | pas d'injection |
| [microsandbox](https://docs.vultr.com/how-to-set-up-agent-sandboxing-on-vultr-cloud-compute) | libkrun, SDK Python / TS / Rust / Go | microVM à partir d'images OCI, trafic entre VM refusé par défaut, serveur MCP de cycle de vie | pas spécifique aux coding agents interactifs |

### Briques de proxy de credentials réutilisables

| Projet | Modèle |
| --- | --- |
| [Infisical Agent Vault](https://infisical.com/blog/agent-vault-the-open-source-credential-proxy-and-vault-for-agents) | forward proxy MITM via `HTTPS_PROXY`, vault intégré, research preview |
| [agent-airlock](https://github.com/es617/agent-airlock) | petit reverse proxy côté hôte, token de proxy révocable, écoute possible sur socket unix, erreurs marquées `X-Agent-Airlock` |
| [OneGate](https://onegate.178-104-9-163.sslip.io/) | gateway MIT, CA racine à approuver, placeholders remplacés par les vrais secrets, politique par agent |
| Aegis (Product Hunt) | proxy local Apache-2.0, vault chiffré, allowlist par domaine, audit, serveur MCP |

### Références produit (non open source ou plateformes)

- **Docker Sandboxes (sbx)** : microVM, proxy hôte `gateway.docker.internal:3128`, CA dans le trust store, `JAVA_TOOL_OPTIONS` pour Java, secrets dans le keychain OS remplacés par des sentinelles. Modèle de référence à reproduire.
- **Modal, Cloudflare Sandboxes, Lens agents** : une CA éphémère par sandbox, installée à la création, variables `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`.
- **kubernetes-sigs/agent-sandbox** : CRDs `Sandbox`, `SandboxTemplate`, `SandboxWarmPool`, `SandboxClaim`, isolation gVisor / Kata ; référence pour le futur backend K8s.
- **Sandboxes natives** : Claude Code `/sandbox` (bubblewrap + socat sur Linux, Seatbelt sur macOS) et Kiro `/sandbox` (IDE 1.3+, CLI 2.31+ en sessions V3 ; Seatbelt, bubblewrap, AppContainer) : garde-fous complémentaires, pas des sandboxes d'agent.

## Risques, pièges et questions ouvertes

Le risque principal n'est pas le sandboxing mais la maintenance des adapters par agent (formats MCP, skills, auth) et le périmètre : sbx est le produit d'une équipe entière.

### Pièges techniques connus

| Piège | Parade |
| --- | --- |
| Volumes en rootless : uid inattendus côté hôte | `--userns=keep-id` |
| SELinux (Fedora, RHEL) : permission denied sur les volumes | suffixe `:Z` |
| Noms d'images courts refusés ou ambigus | noms complets (`docker.io/library/…`) ou registry interne dans `registries.conf` |
| Ports < 1024 interdits en rootless | ports ≥ 1024 |
| Outils qui attendent le socket Docker (Testcontainers) | `systemctl --user enable --now podman.socket`, `DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock`, souvent `TESTCONTAINERS_RYUK_DISABLED=true` ; dans la sandbox, Podman-in-Podman rootless |
| Java ignore `HTTPS_PROXY` et a son propre truststore | `JAVA_TOOL_OPTIONS` + `keytool` |
| Certificate pinning | mode B (reverse proxy) ou passthrough sans injection |
| DNS résolu malgré `--internal` | résolveur filtré + test dédié |
| `--dangerously-skip-permissions` refusé en root (Claude Code) | utilisateur non root dans l'image |
| Repli silencieux des sandboxes natives (Kiro, Claude Code) si la dépendance manque | `sandbox.failIfUnavailable: true` (Claude Code) ; vérifier le statut côté Kiro |
| Bind mounts lents sur macOS (VM `podman machine`) | préférer le mode clone sur macOS |

### Limites assumées de la v1

- Noyau partagé : une faille noyau permet une évasion. Parade future : runtime microVM.
- Tokens OAuth (Claude abonnement, Kiro SSO) présents dans la sandbox.
- Un host autorisé reste un canal d'exfiltration ; l'allowlist limite les destinations, pas le contenu.
- La clé injectée garde tout son périmètre : le proxy contrôle où part la requête, pas ce qu'elle fait. Utiliser des tokens au périmètre minimal.

### Questions ouvertes

- [ ] OS du poste en entreprise (Linux, macOS, Windows/WSL2) ? Conditionne le défaut shared ou clone et la faisabilité de krun.
- [ ] Le format de kit sbx est-il documenté et stable, pour une lecture directe ?
- [ ] sbx expose-t-il ses demandes et décisions réseau en CLI, pour faire marcher le MCP `sandbox-control` avec le `SbxProvider` ?
- [ ] Comment Kiro CLI s'authentifie-t-il exactement (fichier de session SQLite, refresh) et peut-on garder ce token hors de la sandbox ?
- [ ] Le proxy d'entreprise fait-il de l'interception TLS (CA d'entreprise à intégrer au conteneur proxy) ?
- [ ] La plateforme K8s propose-t-elle une `RuntimeClass` gVisor ou Kata ? Est-ce OpenShift (Dev Spaces supporte Kiro en remote SSH) ?
- [ ] Proxy final en Go natif (goproxy, martian) ou mitmproxy conservé ?
- [ ] Nom définitif du projet.

### Hors technique

- **Propriété intellectuelle** : vérifier le contrat de travail (clause PI, projets personnels) avant de développer ou publier en open source un outil utilisé au travail, surtout s'il est développé en partie sur le temps de travail.
- **Validation sécurité** : présenter l'outil à la sécu / IT comme une proposition plutôt qu'un contournement ; un scan d'images et les tests d'évasion en CI sont de bons arguments.

## Références

### Docker Sandboxes (sbx), modèle de référence

- [Docker Sandboxes — Manage credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/)
- [Docker Sandboxes — Troubleshooting (CA, chemins forward / transparent)](https://docs.docker.com/ai/sandboxes/troubleshooting/)
- [Docker Sandboxes — Kiro](https://docs.docker.com/ai/sandboxes/agents/kiro/)
- [Varlock — Docker Sandboxes (gateway, CA, sbx secret)](https://varlock.dev/sandboxes/docker-sandboxes/)
- [DZone — Docker Sandbox isolation story (JAVA\_TOOL\_OPTIONS)](https://dzone.com/articles/docker-sandbox-isolation-story)
- [Docker Labs — Network control and proxy-managed credentials](https://dockerlabs.collabnix.com/docker-workshop/ai-governance/kits/network-and-creds)

### Sandboxes natives des agents

- [Kiro — Local command sandbox](https://kiro.dev/docs/sandbox/)
- [Kiro blog — Local sandboxing](https://kiro.dev/blog/local-sandboxing/)
- [Kiro — Tool permissions (`/tools trust-all`)](https://kiro.dev/docs/cli/chat/permissions/)
- [Classmethod — Test du `/sandbox` de Kiro CLI (mcpSandboxing, sandbox\_network)](https://dev.classmethod.jp/en/articles/kiro-cli-local-command-sandbox/)
- [Claude Code — Sandboxing](https://code.claude.com/docs/en/sandboxing)
- [Claude Code — Development containers](https://docs.claude.com/en/docs/claude-code/devcontainer)

### Injection de credentials et CA par sandbox

- [Modal — Sandbox secret injection](https://modal.com/docs/guide/sandbox-secret-injection)
- [Cloudflare — Credential injection and egress policies for Sandboxes](https://developers.cloudflare.com/changelog/post/2026-04-13-sandbox-outbound-workers-tls-auth/index.md)
- [Lens — Credential bindings (CA éphémère)](https://docs.k8slens.dev/lens-agents/platform-admin/credential-bindings/)
- [Infisical — Agent Vault](https://infisical.com/blog/agent-vault-the-open-source-credential-proxy-and-vault-for-agents)
- [agent-airlock](https://github.com/es617/agent-airlock)
- [StackOne — Pourquoi un proxy ne limite pas ce que la clé peut faire](https://www.stackone.com/blog/ai-agent-sandbox-api-keys/)

### Projets open source (voir Prior art)

- [agentcage](https://github.com/agentcage/agentcage) · [mattolson/agent-sandbox](https://github.com/mattolson/agent-sandbox) · [kuza/agentbox](https://github.com/kuza/agentbox) · [devgugga/agent-sandbox](https://github.com/devgugga/agent-sandbox) · [thomaspeklak/agent-sandbox](https://github.com/thomaspeklak/agent-sandbox) · [thenaithan/agent-sandbox-podman](https://github.com/thenaithan/agent-sandbox-podman) · [ftnilsson/dev-containers](https://github.com/ftnilsson/dev-containers) · [con/yolo](https://github.com/con/yolo) · [statox/claude-devcontainer](https://github.com/statox/claude-devcontainer) · [textcortex/claude-code-sandbox](https://github.com/textcortex/claude-code-sandbox)
- [brood-box (Stacklok)](https://awesome.ecosyste.ms/projects/github.com%2Fstacklok%2Fbrood-box) · [container-use (Dagger)](https://container-use.com/) · [h5i](https://medium.com/@Koukyosyumei/h5i-a-lightweight-rootless-container-sandbox-for-claude-code-and-codex-e9a9eb0cf71e) · [microsandbox (guide Vultr)](https://docs.vultr.com/how-to-set-up-agent-sandboxing-on-vultr-cloud-compute) · [libkrun pour les sandboxes d'agents (iii)](https://iii.dev/blog/why-agent-sandboxes-are-converging-on-libkrun-not-firecracker/)

### Kubernetes

- [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) · [Blog Kubernetes — Running agents with Agent Sandbox](https://kubernetes.io/blog/2026/03/20/running-agents-on-kubernetes-with-agent-sandbox) · [Pulumi — Agent Sandbox (gVisor, Kata)](https://www.pulumi.com/blog/kubernetes-agent-sandbox)
- [Red Hat OpenShift Dev Spaces — support Kiro en SSH](https://docs.redhat.com/en/documentation/red_hat_openshift_dev_spaces/3.26/html/release_notes_and_known_issues/technology-preview) · [Coder — Kiro CLI dans des workspaces](https://coder.com/blog/using-kiro-cli-with-coder-workspaces)
