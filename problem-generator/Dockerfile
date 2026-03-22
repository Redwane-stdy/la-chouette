# =============================================================================
# La Chouette — Problem Generator — Dockerfile
# =============================================================================
#
# Build multi-stage pour minimiser la taille de l'image finale :
#   - Stage 1 (builder)  : image Go officielle, compile le binaire statique
#   - Stage 2 (runtime)  : image Alpine minimale, contient uniquement le binaire
#
# L'image finale fait ~15 Mo au lieu de ~800 Mo avec l'image Go complète.
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1 : Compilation
# Utilise l'image officielle Go 1.22 sur Alpine pour un build léger.
# -----------------------------------------------------------------------------
FROM golang:1.22-alpine AS builder

# Répertoire de travail dans le container de build.
WORKDIR /app

# Copie des fichiers de dépendances en premier pour exploiter le cache Docker.
# Si go.mod et go.sum ne changent pas, `go mod download` ne sera pas réexécuté.
COPY go.mod go.sum ./

# Téléchargement des dépendances (github.com/lib/pq et github.com/rs/zerolog).
RUN go mod download

# Copie du reste des sources après les dépendances (ordre optimisé pour le cache).
COPY . .

# Compilation du binaire Go en mode statique.
# CGO_ENABLED=0 : pas de liaison dynamique → binaire autonome sur Alpine.
# -ldflags="-w -s" : supprime les symboles de debug → binaire plus petit.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s" \
    -o problem-generator \
    ./cmd/main.go

# -----------------------------------------------------------------------------
# Stage 2 : Image finale minimale
# Alpine 3.19 : ~5 Mo de base, pas de compilateur ni d'outils inutiles.
# -----------------------------------------------------------------------------
FROM alpine:3.19

# Métadonnées de l'image.
LABEL maintainer="La Chouette Project"
LABEL description="Générateur de système d'équations linéaires pour La Chouette"

# Répertoire de travail dans le container de production.
WORKDIR /app

# Copie uniquement du binaire compilé depuis le stage builder.
# Rien d'autre n'est nécessaire : le binaire est statiquement lié.
COPY --from=builder /app/problem-generator .

# Variables d'environnement avec valeurs par défaut.
# Elles peuvent être surchargées via docker-compose ou docker run -e.
ENV POSTGRES_DSN="postgres://lachouette:secret@postgres:5432/lachouette?sslmode=disable"
ENV NUM_UNKNOWNS=50
ENV NUM_EQUATIONS=80
ENV DEMO_MODE=false
ENV LOG_LEVEL=info

# Le service s'exécute UNE SEULE FOIS et se termine proprement (EXIT 0).
# Pas de port exposé : ce n'est pas un serveur HTTP.
CMD ["./problem-generator"]
