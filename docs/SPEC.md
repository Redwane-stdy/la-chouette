# La Chouette — Spécification Technique Complète
> Staff Engineer: Document de référence v1.0 — 2026-03-21

---

## 1. Vision Produit

**La Chouette** est un moteur de corrélation en temps réel qui simule un détective algorithmique.
N sources de données hétérogènes publient des **faits partiels** (équations linéaires) en continu.
Le moteur accumule ces faits, réduit progressivement l'espace des solutions, et propose les
scénarios les plus probables jusqu'à résoudre complètement le système.

> But pédagogique : démontrer qu'un système distribué event-driven peut résoudre un problème
> mathématiquement complexe (système de 50 équations à 50 inconnues) de façon incrémentale,
> tolérant le bruit, la latence variable et les sources hétérogènes.

---

## 2. Architecture Globale

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          DOCKER-COMPOSE NETWORK                         │
│                                                                         │
│  ┌──────────────────┐                                                   │
│  │ problem-generator│──► PostgreSQL (problem_definition, equations)     │
│  │     (Go)         │    [Exécution unique au démarrage]                │
│  └──────────────────┘                                                   │
│                                                                         │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐                │
│  │ source-alpha │   │ source-beta  │   │ source-gamma │                │
│  │    (Go)      │   │   (C# .NET)  │   │    (Go)      │                │
│  │  every ~3s   │   │  every ~4s   │   │  every ~5s   │                │
│  └──────┬───────┘   └──────┬───────┘   └──────┬───────┘                │
│         │                  │                  │                         │
│         └──────────────────┴──────────────────┘                        │
│                            │ topic: equations                           │
│                            ▼                                            │
│                    ┌───────────────┐                                    │
│  source-noise ──►  │     KAFKA     │  ◄── topic: noise                  │
│    (Go, ~2s)       │               │                                    │
│                    └───────┬───────┘                                    │
│                            │                                            │
│              ┌─────────────┘                                            │
│              ▼                                                           │
│  ┌───────────────────────┐                                              │
│  │   la-chouette-engine  │──► PostgreSQL (constraint_state,             │
│  │        (Go)           │    resolved_variables, investigation_state)  │
│  │  Gaussian Elimination │──► topic: results                            │
│  │  Constraint Propagation│                                             │
│  └───────────────────────┘                                              │
│                                                                         │
│  ┌───────────────────────┐                                              │
│  │      API (C# .NET)    │◄── PostgreSQL (read)                         │
│  │   REST + WebSocket     │◄── topic: results (subscribe)               │
│  └───────────┬───────────┘                                              │
│              │                                                           │
│              ▼                                                           │
│  ┌───────────────────────┐                                              │
│  │  Dashboard (HTML/JS)  │   Polling API toutes les 500ms               │
│  │  Noir & Blanc         │   Affichage temps réel                       │
│  └───────────────────────┘                                              │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Kafka Topics & Message Schemas

### 3.1 Topic `equations`
Messages publiés par source-alpha, source-beta, source-gamma.

```json
{
  "source": "alpha",
  "equation_id": "eq_023",
  "coefficients": {
    "x1": 2.0,
    "x3": -1.5,
    "x47": 3.0
  },
  "constant": 14.5,
  "timestamp": "2026-03-21T10:30:00.000Z"
}
```

### 3.2 Topic `noise`
Messages publiés par source-noise (ignorés par le moteur, juste loggés).

```json
{
  "source": "noise",
  "type": "news",
  "headline": "Les marchés financiers en légère hausse ce matin",
  "body": "Selon les analystes, la tendance devrait se maintenir...",
  "timestamp": "2026-03-21T10:30:00.000Z"
}
```

### 3.3 Topic `results`
Messages publiés par la-chouette-engine vers l'API.

```json
{
  "type": "VARIABLE_RESOLVED | PROGRESS_UPDATE | SYSTEM_SOLVED",
  "variable_name": "x17",
  "value": 4.200000,
  "equations_received": 47,
  "variables_resolved": 12,
  "variables_total": 50,
  "completion_percentage": 24.0,
  "status": "COLLECTING | CORRELATING | CONVERGING | SOLVED",
  "status_message": "Corrélation en cours — 12 variables identifiées sur 50",
  "timestamp": "2026-03-21T10:30:00.000Z"
}
```

---

## 4. Schéma PostgreSQL

### Base de données : `lachouette`

```sql
-- Problème généré au démarrage
CREATE TABLE problem_variables (
    id          SERIAL PRIMARY KEY,
    name        VARCHAR(10) NOT NULL,  -- 'x1', 'x2', ...
    true_value  DOUBLE PRECISION NOT NULL,
    created_at  TIMESTAMP DEFAULT NOW()
);

-- Équations générées au démarrage
CREATE TABLE problem_equations (
    id              SERIAL PRIMARY KEY,
    equation_id     VARCHAR(20) NOT NULL,
    coefficients    JSONB NOT NULL,  -- {"x1": 2.0, "x3": -1.5}
    constant        DOUBLE PRECISION NOT NULL,
    assigned_source VARCHAR(20),     -- 'alpha', 'beta', 'gamma'
    dispatch_order  INT NOT NULL,    -- ordre d'envoi
    created_at      TIMESTAMP DEFAULT NOW()
);

-- Équations reçues par le moteur
CREATE TABLE equations_received (
    id          SERIAL PRIMARY KEY,
    equation_id VARCHAR(20) NOT NULL,
    source      VARCHAR(20) NOT NULL,
    coefficients JSONB NOT NULL,
    constant    DOUBLE PRECISION NOT NULL,
    received_at TIMESTAMP DEFAULT NOW()
);

-- Variables résolues par le moteur
CREATE TABLE resolved_variables (
    id              SERIAL PRIMARY KEY,
    variable_name   VARCHAR(10) NOT NULL UNIQUE,
    computed_value  DOUBLE PRECISION NOT NULL,
    true_value      DOUBLE PRECISION,
    equations_count INT NOT NULL,  -- nombre d'équations au moment de la résolution
    resolved_at     TIMESTAMP DEFAULT NOW()
);

-- État global de l'investigation
CREATE TABLE investigation_state (
    id                  SERIAL PRIMARY KEY DEFAULT 1,
    status              VARCHAR(20) NOT NULL DEFAULT 'COLLECTING',
    equations_received  INT NOT NULL DEFAULT 0,
    variables_resolved  INT NOT NULL DEFAULT 0,
    variables_total     INT NOT NULL DEFAULT 50,
    completion_pct      DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    started_at          TIMESTAMP DEFAULT NOW(),
    solved_at           TIMESTAMP,
    updated_at          TIMESTAMP DEFAULT NOW()
);

-- Log des messages de bruit
CREATE TABLE noise_log (
    id          SERIAL PRIMARY KEY,
    source      VARCHAR(30) NOT NULL,
    headline    TEXT NOT NULL,
    received_at TIMESTAMP DEFAULT NOW()
);
```

---

## 5. API REST Endpoints (C# .NET)

```
GET  /api/status
     → { status, statusMessage, equationsReceived, variablesResolved,
          variablesTotal, completionPct, startedAt, elapsedSeconds }

GET  /api/variables
     → [ { name, status: "RESOLVED|UNKNOWN", computedValue, trueValue,
            resolvedAt, equationsAtResolution } ]

GET  /api/equations
     → [ { equationId, source, coefficients, constant, receivedAt } ]
     query params: ?limit=50&offset=0

GET  /api/noise
     → [ { source, headline, receivedAt } ]
     query params: ?limit=20

GET  /api/metrics
     → { equationsPerSecond, variablesPerMinute, avgResolutionTime,
          rejectionRate, totalEquations, totalNoise, uptimeSeconds }

GET  /api/live           (WebSocket upgrade ou SSE)
     → stream d'events results

POST /api/experiment/reset
     Body: { "confirm": true }
     → Vide toutes les tables de state (PAS problem_definition)
     → { "success": true, "message": "State cleared" }

DELETE /api/experiment/purge
     Body: { "confirm": true, "deleteEverything": true }
     → Supprime toutes les données y compris problem_definition
     → { "success": true }
```

---

## 6. Services — Responsabilités

### 6.1 problem-generator (Go)
- Génère N=50 inconnues avec valeurs vraies aléatoires dans [-10, 10]
- Génère M=80 équations linéaires garantissant un système soluble (matrice plein rang)
- Assigne chaque équation à une source (alpha/beta/gamma) de façon équilibrée
- Persiste en PostgreSQL et s'arrête
- **Idempotent** : si les données existent déjà, ne re-génère pas

### 6.2 source-alpha (Go)
- Lit les équations assignées à `alpha` depuis PostgreSQL (ordre `dispatch_order`)
- Publie chaque équation sur topic `equations` toutes les 3s (±0.5s jitter)
- Log coloré : BLEU `[ALPHA]`

### 6.3 source-beta (C# .NET)
- Même logique que alpha, équations assignées à `beta`
- Publie toutes les 4s (±1s jitter)
- Log coloré : VERT `[BETA]`

### 6.4 source-gamma (Go)
- Même logique, équations assignées à `gamma`
- Publie toutes les 5s (±1s jitter)
- Log coloré : ORANGE `[GAMMA]`

### 6.5 source-noise (Go)
- Publie des news fictives sur topic `noise` toutes les 2-7s (aléatoire)
- 30 headlines prédéfinies en rotation aléatoire
- Log coloré : GRIS `[NOISE]`

### 6.6 la-chouette-engine (Go)
- Double consumer Kafka : topic `equations` + topic `noise`
- Pour `equations` : algorithme d'élimination gaussienne incrémentale
- Pour `noise` : log uniquement + persist en noise_log
- Publie sur topic `results` après chaque traitement
- Persiste état dans PostgreSQL
- Log coloré : CYAN `[CHOUETTE]`

**Algorithme Gaussian Elimination Incrémentale :**
1. Maintenir la matrice augmentée [A|b] en mémoire
2. À chaque nouvelle équation : ajout de ligne, pivot partiel
3. Si une variable devient déterminable (1 seule inconnue dans une ligne) → résoudre
4. Propagation : substituer la valeur résolue dans toutes les autres lignes
5. Répéter jusqu'à convergence

**États :**
- `COLLECTING`  : < 20 équations reçues
- `CORRELATING` : 20-49 équations, ≥ 1 variable résolue
- `CONVERGING`  : ≥ 50 équations, > 30% variables résolues
- `SOLVED`      : toutes les variables résolues

### 6.7 api (C# .NET)
- REST API sur port 8080
- Lit depuis PostgreSQL (read-only sur les tables de state)
- Consomme topic `results` pour SSE/WebSocket live
- CORS ouvert pour le dashboard local

### 6.8 dashboard (HTML + CSS + JS vanilla)
- Servi par Nginx sur port 3000
- Palette : noir (#000), blanc (#FFF), gris (#888)
- Police monospace (Courier New / monospace)
- Polling API toutes les 500ms
- 3 panneaux : STATUS | VARIABLES | LOG TERMINAL
- Bouton "EXPÉRIENCE TERMINÉE" avec confirmation modale

---

## 7. Configuration & Variables d'Environnement

| Variable | Défaut | Description |
|---|---|---|
| `KAFKA_BROKERS` | `kafka:9092` | Adresse(s) broker Kafka |
| `POSTGRES_DSN` | `postgres://lachouette:secret@postgres:5432/lachouette` | DSN PostgreSQL |
| `NUM_UNKNOWNS` | `50` | Nombre d'inconnues |
| `NUM_EQUATIONS` | `80` | Nombre d'équations à générer |
| `DEMO_MODE` | `false` | Si `true` : NUM_UNKNOWNS=15, NUM_EQUATIONS=25, intervals /3 |
| `SOURCE_ALPHA_INTERVAL_MS` | `3000` | Intervalle source alpha (ms) |
| `SOURCE_BETA_INTERVAL_MS` | `4000` | Intervalle source beta (ms) |
| `SOURCE_GAMMA_INTERVAL_MS` | `5000` | Intervalle source gamma (ms) |
| `SOURCE_NOISE_INTERVAL_MIN_MS` | `2000` | Intervalle bruit min (ms) |
| `SOURCE_NOISE_INTERVAL_MAX_MS` | `7000` | Intervalle bruit max (ms) |
| `API_PORT` | `8080` | Port API REST |
| `DASHBOARD_PORT` | `3000` | Port Dashboard |
| `LOG_LEVEL` | `INFO` | Niveau de log |

---

## 8. Séquence de Démarrage (docker-compose)

```
1. postgres        → démarre, attend healthcheck
2. kafka           → démarre, attend healthcheck
3. problem-generator → lit postgres, génère le problème, s'arrête (restart: no)
4. source-alpha    → attend problem-generator, commence à publier (depends_on)
5. source-beta     → idem
6. source-gamma    → idem
7. source-noise    → démarre en parallèle
8. la-chouette-engine → attend kafka + postgres, commence à consommer
9. api             → attend postgres + la-chouette-engine
10. dashboard      → disponible immédiatement
```

**Logs attendus au démarrage (dans l'ordre) :**
```
[INIT]     PostgreSQL ready
[INIT]     Kafka ready
[GEN]      Génération du problème : 50 inconnues, 80 équations
[GEN]      Variable x1=3.14, x2=-7.82, x3=1.09 ... (toutes)
[GEN]      Équation eq_001 assignée à alpha : 2*x1 + (-1.5)*x3 = 4.155
[GEN]      ...
[GEN]      Problème généré et persisté. Démarrage des sources dans 5s...
[ALPHA]    Prêt. Publication dans 3s.
[BETA]     Prêt. Publication dans 4s.
[GAMMA]    Prêt. Publication dans 5s.
[NOISE]    Prêt. Pollution en cours...
[CHOUETTE] Moteur démarré. En attente d'équations...
[ALPHA]    → eq_001 publié : 2.00*x1 + (-1.50)*x3 = 4.16
[CHOUETTE] eq_001 reçu | Variables connues: 0/50 | Statut: COLLECTING
[NOISE]    📰 "La Fed maintient ses taux directeurs"
...
[CHOUETTE] Variable x17 = 4.20 ✓ (47 équations | 12 variables résolues)
...
[CHOUETTE] *** SYSTÈME RÉSOLU *** Toutes les 50 variables trouvées
```

---

## 9. Durée de l'Expérience

| Mode | Inconnues | Équations | Durée estimée |
|---|---|---|---|
| Normal | 50 | 80 | ~5-6 minutes |
| Demo | 15 | 25 | ~1 minute |
| Fast Demo | 15 | 25 | ~20 secondes |

**Activer Demo mode :** `DEMO_MODE=true` dans `.env`

---

## 10. Nettoyage

- **Reset (garder le problème)** : `POST /api/experiment/reset` — vide les tables de state
- **Purge totale** : `DELETE /api/experiment/purge` — supprime tout
- **UI** : Bouton "EXPÉRIENCE TERMINÉE" sur le dashboard avec modale de confirmation

---

## 11. Stack Technique

| Composant | Technologie | Justification |
|---|---|---|
| problem-generator | Go 1.22 | Calcul matriciel, init rapide |
| source-alpha | Go 1.22 | Léger, goroutines pour timing précis |
| source-beta | C# .NET 8 | Démontre l'hétérogénéité des sources |
| source-gamma | Go 1.22 | Idem source-alpha |
| source-noise | Go 1.22 | Léger |
| la-chouette-engine | Go 1.22 | Performance, goroutines, lib `gonum` pour algèbre linéaire |
| api | C# .NET 8 | ASP.NET Core minimal API, SignalR pour WebSocket |
| dashboard | HTML/CSS/JS vanilla | Zéro dépendance, servi par Nginx |
| Message broker | Kafka (Confluent) | Event-driven, durable, multi-consumer |
| Base de données | PostgreSQL 16 | Gratuit, robuste, JSONB pour coefficients |
| Containerisation | Docker + docker-compose | One-command startup |

---

## 12. Structure du Dépôt

```
la-chouette/
├── README.md
├── docker-compose.yml
├── .env.example
├── docs/
│   ├── SPEC.md                    ← ce document
│   └── contracts/
│       ├── kafka-messages.md
│       ├── db-schema.sql
│       └── api-spec.md
├── infra/
│   ├── kafka/
│   │   └── create-topics.sh
│   └── postgres/
│       └── init.sql
├── problem-generator/
│   ├── cmd/main.go
│   ├── go.mod
│   └── Dockerfile
├── source-alpha/
│   ├── main.go
│   ├── go.mod
│   └── Dockerfile
├── source-beta/
│   ├── Program.cs
│   ├── source-beta.csproj
│   └── Dockerfile
├── source-gamma/
│   ├── main.go
│   ├── go.mod
│   └── Dockerfile
├── source-noise/
│   ├── main.go
│   ├── go.mod
│   └── Dockerfile
├── la-chouette-engine/
│   ├── cmd/main.go
│   ├── internal/
│   │   ├── solver/      ← Gaussian elimination
│   │   ├── consumer/    ← Kafka consumers
│   │   └── store/       ← PostgreSQL persistence
│   ├── go.mod
│   └── Dockerfile
├── api/
│   ├── Program.cs
│   ├── Controllers/
│   ├── api.csproj
│   └── Dockerfile
├── dashboard/
│   ├── index.html
│   ├── style.css
│   ├── app.js
│   └── nginx.conf
└── scripts/
    ├── reset.sh          ← POST /api/experiment/reset
    └── purge.sh          ← DELETE /api/experiment/purge
```
