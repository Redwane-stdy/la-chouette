# La Chouette — Moteur de corrélation en temps réel

![Go](https://img.shields.io/badge/Go-1.22-00ADD8?style=flat-square&logo=go&logoColor=white)
![.NET](https://img.shields.io/badge/.NET-10.0-512BD4?style=flat-square&logo=dotnet&logoColor=white)
![Kafka](https://img.shields.io/badge/Apache_Kafka-Confluent_7.5-231F20?style=flat-square&logo=apachekafka&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=flat-square&logo=postgresql&logoColor=white)
![Docker](https://img.shields.io/badge/Docker_Compose-v2-2496ED?style=flat-square&logo=docker&logoColor=white)

```
 ██╗      █████╗      ██████╗██╗  ██╗ ██████╗ ██╗   ██╗███████╗████████╗████████╗███████╗
 ██║     ██╔══██╗    ██╔════╝██║  ██║██╔═══██╗██║   ██║██╔════╝╚══██╔══╝╚══██╔══╝██╔════╝
 ██║     ███████║    ██║     ███████║██║   ██║██║   ██║█████╗     ██║      ██║   █████╗
 ██║     ██╔══██║    ██║     ██╔══██║██║   ██║██║   ██║██╔══╝     ██║      ██║   ██╔══╝
 ███████╗██║  ██║    ╚██████╗██║  ██║╚██████╔╝╚██████╔╝███████╗   ██║      ██║   ███████╗
 ╚══════╝╚═╝  ╚═╝     ╚═════╝╚═╝  ╚═╝ ╚═════╝  ╚═════╝ ╚══════╝   ╚═╝      ╚═╝   ╚══════╝
```

> *"Un détective algorithmique qui résout l'impossible, une équation à la fois."*

**La Chouette** est un moteur de corrélation distribué en temps réel. Il simule un détective algorithmique qui résout un système d'équations linéaires à mesure que les indices arrivent de sources distribuées — exactement comme une enquête où les témoignages arrivent dans le désordre, à intervalles irréguliers, mêlés à du bruit.

---

## Table des matières

- [Dépendances](#dépendances)
- [Lancement rapide — Mode démo](#lancement-rapide--mode-démo)
- [Arrêter le système](#arrêter-le-système)
- [Choisir son mode : démo ou problème personnalisé](#choisir-son-mode--démo-ou-problème-personnalisé)
- [Configurer le problème à résoudre](#configurer-le-problème-à-résoudre)
- [Comment ça marche](#comment-ça-marche)
- [Architecture](#architecture)
- [API REST](#api-rest)
- [Structure du projet](#structure-du-projet)

---

## Dépendances

**Une seule dépendance obligatoire : Docker Desktop.**

| Outil | Version minimale | Installation |
|---|---|---|
| **Docker Desktop** | 24.0+ | [docker.com/get-started](https://www.docker.com/get-started) |

Docker Desktop inclut Docker Compose v2, qui est la seule autre dépendance nécessaire.

**Aucune installation de Go, .NET, Kafka ou PostgreSQL n'est requise.** Tout tourne dans des conteneurs.

### Compatibilité

| OS | Statut |
|---|---|
| macOS (Apple Silicon & Intel) | ✅ Testé |
| Linux | ✅ Compatible |
| Windows (avec WSL2) | ✅ Compatible |

---

## Lancement rapide — Mode démo

Le mode démo résout un système de **15 inconnues / 25 équations** en environ **60 secondes**.

```bash
# 1. Cloner le projet
git clone https://github.com/Redwane-stdy/la-chouette.git
cd la-chouette

# 2. Lancer tout le système (build + démarrage)
docker compose up --build
```

> Le premier build télécharge les images Docker et compile les services (~3-5 min).
> Les lancements suivants sont instantanés grâce au cache Docker.

```bash
# 3. Ouvrir le dashboard dans votre navigateur
open http://localhost:3000        # macOS
# ou manuellement : http://localhost:3000
```

```bash
# 4. Cliquer le bouton START sur le dashboard
# Les sources attendent votre signal avant de publier — vous avez le temps d'ouvrir la page.
```

**Ce que vous verrez :**

1. **Dashboard en attente** → gros bouton START + explication du système
2. **Clic START** → les 3 sources commencent à publier des équations
3. **En direct** → variables résolues une à une avec leurs valeurs (`x7 = -3.4058 ✓ err:0.000000`)
4. **~60s plus tard** → `★ SYSTÈME RÉSOLU ★` — toutes les variables trouvées avec erreur absolue = 0

> 📸 *Capture d'écran à insérer : dashboard en état WAITING avec bouton START*

> 📸 *Capture d'écran à insérer : dashboard pendant la résolution — variables apparaissent en vert*

> 📸 *Capture d'écran à insérer : dashboard SOLVED — 100% des variables résolues*

---

## Arrêter le système

### Arrêt simple (conserve les données)

```bash
docker compose down
```

Au prochain `docker compose up`, les sources reprennent depuis le début (le flag dispatched est réinitialisé automatiquement).

### Arrêt complet + suppression des données

```bash
docker compose down -v
```

Supprime le volume PostgreSQL. Au prochain démarrage, un nouveau problème mathématique est généré avec de nouveaux coefficients aléatoires.

### Tout supprimer — conteneurs, volumes et images

```bash
docker compose down -v --rmi local
```

Remet la machine dans l'état initial. Le prochain `docker compose up --build` repart de zéro.

---

## Choisir son mode : démo ou problème personnalisé

Le comportement du système est contrôlé par la variable `DEMO_MODE` dans le fichier `.env` :

### Mode démo (par défaut)

```env
DEMO_MODE=true
```

- **15 inconnues** (x1 … x15), **25 équations**
- Intervalles entre publications **divisés par 3** (environ 1 équation/seconde)
- **Durée totale : ~60 secondes**
- Idéal pour démontrer le système rapidement

### Mode complet

```env
DEMO_MODE=false
```

- **50 inconnues** (x1 … x50), **80 équations** (valeurs par défaut, configurables)
- Intervalles normaux (3-5 secondes entre équations)
- **Durée totale : ~5-7 minutes**

Pour passer d'un mode à l'autre, éditez `.env` puis relancez proprement :

```bash
docker compose down -v
docker compose up --build
```

> Le `-v` est important : il efface le volume PostgreSQL pour que le `problem-generator` crée un nouveau problème adapté au mode choisi.

---

## Configurer le problème à résoudre

Éditez le fichier `.env` à la racine du projet. Ces paramètres ne s'appliquent que lorsque `DEMO_MODE=false` :

```env
# Nombre d'inconnues dans le système (x1 … xN)
NUM_UNKNOWNS=50

# Nombre total d'équations générées
# Doit être supérieur à NUM_UNKNOWNS pour que le système soit surdéterminé (recommandé : 1.5× à 2×)
NUM_EQUATIONS=80
```

### Exemples de configurations

| Difficulté | `NUM_UNKNOWNS` | `NUM_EQUATIONS` | Durée estimée |
|---|---|---|---|
| Petit | 10 | 20 | ~30s |
| Moyen | 25 | 40 | ~2-3 min |
| Standard | 50 | 80 | ~5-7 min |
| Grand | 100 | 160 | ~12-15 min |

### Vitesse de publication

Chaque source publie ses équations selon ces intervalles (en millisecondes) :

```env
SOURCE_ALPHA_INTERVAL_MS=3000   # 1 équation toutes les 3s
SOURCE_BETA_INTERVAL_MS=4000    # 1 équation toutes les 4s
SOURCE_GAMMA_INTERVAL_MS=5000   # 1 équation toutes les 5s
```

Réduisez ces valeurs pour accélérer l'expérience sans activer `DEMO_MODE`.

### Appliquer les changements

```bash
docker compose down -v    # supprime les données de l'ancienne expérience
docker compose up --build # relance avec la nouvelle configuration
```

---

## Comment ça marche

### Le scénario

Un **problème mathématique** est généré au démarrage : N inconnues (x1, x2, …, xN) avec des valeurs secrètes tirées aléatoirement dans [-10, 10]. Des équations linéaires `a₁x₁ + a₂x₂ + … = b` sont créées de façon à avoir une solution exacte connue.

Ces équations sont distribuées entre 3 sources qui les publient sur Kafka à intervalles irréguliers. Le moteur les reçoit, construit la matrice de contraintes, et résout les variables dès qu'il en a assez — avec une erreur absolue de 0.

### Les services

```
[problem-generator]  Génère les N inconnues et M équations. Se termine et exit 0.

[source-alpha]       Publie ~40% des équations sur Kafka (topic: equations)
[source-beta]        Publie ~30% des équations sur Kafka
[source-gamma]       Publie ~30% des équations sur Kafka
                     → Les 3 sources ATTENDENT que vous cliquiez START sur le dashboard.

[source-noise]       Injecte des fausses pistes (headlines financières) sur le topic
                     "noise". Le moteur les détecte et les ignore — bruit simulé.

[la-chouette-engine] Cœur du système. Reçoit les équations, construit la matrice Ax=b,
                     et applique l'élimination gaussienne avec pivot partiel. Persiste
                     les résultats dans PostgreSQL. Erreur attendue : 0.000000.

[api]                API REST C#/.NET 10. Expose l'état en temps réel sur le port 8080.

[dashboard]          Interface web noir & blanc sur le port 3000.
                     Se met à jour toutes les 700ms via polling.
```

### L'algorithme — Élimination gaussienne incrémentale

À chaque équation reçue via Kafka :

1. Ajout de la ligne à la matrice augmentée `[A|b]`
2. Substitution des variables déjà résolues (réduction de la matrice)
3. Dès que le nombre d'équations ≥ inconnues restantes : **élimination gaussienne complète** avec pivot partiel (stabilité numérique)
4. Détection des lignes à un seul coefficient non-nul → résolution directe
5. Propagation en cascade des valeurs trouvées

**Résultat :** erreur absolue = 0.000000 pour toutes les variables. Le moteur retrouve la solution exacte.

### États de l'investigation

| Statut | Condition | Affiché dans le dashboard |
|---|---|---|
| `COLLECTING` | < 20 équations reçues | Récolte d'informations... |
| `CORRELATING` | ≥ 20 équations, < 30% résolues | Corrélation en cours |
| `CONVERGING` | ≥ 30% variables résolues | Convergence détectée |
| `SOLVED` | 100% variables résolues | ★ SYSTÈME RÉSOLU ★ |

---

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                         INFRASTRUCTURE                               │
│  PostgreSQL 16       Zookeeper 7.5       Kafka 7.5 (Confluent)      │
└─────────────────────────┬────────────────────────────────────────────┘
                          │
               ┌──────────▼──────────┐
               │    kafka-init       │  crée topics: equations, noise, results
               └──────────┬──────────┘
                          │
               ┌──────────▼──────────┐
               │  problem-generator  │  génère N inconnues + M équations en BDD
               └────┬────────────────┘
                    │ (attend START sur le dashboard)
       ┌────────────┼────────────┬───────────────────────────────┐
       │            │            │                               │
┌──────▼──────┐ ┌───▼────┐ ┌────▼────────┐           ┌──────────▼──────┐
│source-alpha │ │source- │ │source-gamma │           │  source-noise   │
│    (Go)     │ │beta(C#)│ │    (Go)     │           │     (Go)        │
└──────┬──────┘ └───┬────┘ └────┬────────┘           └──────────┬──────┘
       │  topic: equations      │                               │ topic: noise
       └────────────────────────┘                               │
                    │                                            │
         ┌──────────▼────────────────────────────────────────────▼──────┐
         │              La Chouette Engine (Go)                          │
         │  Élimination gaussienne incrémentale — persistance PostgreSQL │
         └─────────────────────┬─────────────────────────────────────────┘
                               │
                    ┌──────────▼──────────┐
                    │  API REST (C#/.NET) │  :8080
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  Dashboard (nginx)  │  :3000
                    └─────────────────────┘
```

---

## API REST

Base URL : `http://localhost:8080`

| Méthode | Endpoint | Description |
|---|---|---|
| `GET` | `/api/status` | État global : statut, compteurs, dates, pourcentage |
| `GET` | `/api/variables` | Toutes les variables avec valeurs calculées et erreur absolue |
| `GET` | `/api/equations` | Équations reçues (source, coefficients, constante) |
| `GET` | `/api/noise` | Messages de bruit (`source` + `text` en clair) |
| `GET` | `/api/metrics` | Métriques : équations/s, taux de résolution, durée |
| `GET` | `/api/live` | Flux Server-Sent Events — utilisé par le dashboard |
| `GET` | `/api/experiment/ready` | `{"ready": true\|false}` — expérience démarrée ? |
| `POST` | `/api/experiment/start` | Démarre l'expérience (équivalent du bouton START) |
| `POST` | `/api/experiment/reset` | Remet les compteurs à zéro, conserve le problème |
| `DELETE` | `/api/experiment/purge` | Supprime toutes les données (irréversible) |

### Exemples curl

```bash
# État global
curl http://localhost:8080/api/status

# Démarrer l'expérience sans passer par le dashboard
curl -X POST http://localhost:8080/api/experiment/start

# Voir les variables résolues
curl http://localhost:8080/api/variables

# Voir les équations reçues
curl http://localhost:8080/api/equations

# Voir le bruit filtré
curl http://localhost:8080/api/noise
```

### Inspecter la base de données directement

```bash
# État de l'investigation
docker exec -it lachouette_postgres psql -U lachouette -d lachouette \
  -c "SELECT status, equations_received, variables_resolved, completion_pct FROM investigation_state;"

# Variables résolues avec leurs valeurs
docker exec -it lachouette_postgres psql -U lachouette -d lachouette \
  -c "SELECT variable_name, computed_value, true_value, absolute_error FROM resolved_variables ORDER BY variable_name;"
```

---

## Structure du projet

```
la-chouette/
│
├── docker-compose.yml              # Orchestration des 12 services
├── .env.example                    # Template de configuration (copier en .env)
│
├── infra/
│   ├── kafka/
│   │   └── create-topics.sh        # Crée les topics: equations, noise, results
│   └── postgres/
│       └── init.sql                # Schéma BDD (6 tables, vue, trigger)
│
├── scripts/
│   ├── reset.sh                    # Reset via API (interactif)
│   └── purge.sh                    # Purge complète (double confirmation)
│
├── problem-generator/              # Go 1.22 — génère le problème mathématique
├── source-alpha/                   # Go 1.22 — publie ~40% des équations
├── source-beta/                    # C# .NET 10 — publie ~30% des équations
├── source-gamma/                   # Go 1.22 — publie ~30% des équations
├── source-noise/                   # Go 1.22 — injecte du bruit
│
├── la-chouette-engine/             # Go 1.22 — moteur de résolution
│   └── internal/
│       ├── consumer/               # Consumers Kafka
│       ├── solver/gaussian.go      # Élimination gaussienne avec pivot partiel
│       └── store/postgres.go       # Persistance PostgreSQL
│
├── api/                            # C# .NET 10 — API REST
│   └── Program.cs                  # 10 endpoints, Npgsql, SSE
│
└── dashboard/                      # Interface web
    ├── index.html / style.css / app.js
    └── nginx.conf                  # Proxy /api/ → api:8080
```

---

## Technologies

| Technologie | Version | Usage |
|---|---|---|
| **Go** | 1.22 | problem-generator, sources alpha/gamma/noise, engine |
| **C# / .NET** | 10.0 | source-beta, API REST |
| **Apache Kafka** | 7.5.0 (Confluent) | Bus de messages (topics: equations, noise, results) |
| **PostgreSQL** | 16-alpine | Persistance du problème, résultats, état |
| **zerolog** | 1.33.0 | Logs structurés et colorés (services Go) |
| **kafka-go** | 0.4.47 | Client Kafka natif Go |
| **Confluent.Kafka** | 2.x | Client Kafka C# |
| **Npgsql** | 8.x | Driver PostgreSQL C# |
| **nginx** | alpine | Sert le dashboard + proxy API |
| **Docker Compose** | v2 | Orchestration locale complète |

---

*La Chouette voit tout. Même dans l'obscurité des données distribuées.*
