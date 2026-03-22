# =============================================================================
# Dockerfile — source-alpha
# Multi-stage build : compilation Go → image minimale alpine
# =============================================================================

# ── Stage 1 : compilation ────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Métadonnées
LABEL stage="builder"
LABEL service="source-alpha"

# Installation des dépendances système pour la compilation (git requis par go mod)
RUN apk add --no-cache git ca-certificates

# Répertoire de travail dans le conteneur de build
WORKDIR /build

# Copie des fichiers de dépendances en premier pour profiter du cache Docker
COPY go.mod go.sum ./

# Téléchargement des dépendances Go (mise en cache si go.mod/go.sum inchangés)
RUN go mod download

# Copie du code source
COPY main.go .

# Compilation statique : CGO désactivé pour un binaire portable sans libc
# -ldflags="-w -s" : supprime les infos de debug pour réduire la taille du binaire
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o /source-alpha .

# ── Stage 2 : image finale minimale ──────────────────────────────────────────
FROM alpine:3.19

# Métadonnées du service
LABEL service="source-alpha"
LABEL role="producer"
LABEL project="la-chouette"

# Certificats CA nécessaires pour les connexions TLS éventuelles
RUN apk add --no-cache ca-certificates tzdata

# Utilisateur non-root pour la sécurité
RUN addgroup -S lachouette && adduser -S lachouette -G lachouette

# Copie du binaire compilé depuis le stage builder
COPY --from=builder /source-alpha /usr/local/bin/source-alpha

# L'application tourne sous l'utilisateur non-root
USER lachouette

# Variables d'environnement avec valeurs par défaut
ENV KAFKA_BROKERS=kafka:9092 \
    POSTGRES_DSN="" \
    SOURCE_ALPHA_INTERVAL_MS=3000 \
    DEMO_MODE=false \
    LOG_LEVEL=INFO

# Point d'entrée du service
ENTRYPOINT ["/usr/local/bin/source-alpha"]
