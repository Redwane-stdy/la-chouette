# =============================================================================
# Dockerfile — la-chouette-engine
# Multi-stage build : compilation Go 1.22 puis image minimale alpine:3.19
#
# Stage 1 (builder) : compile le binaire statique sans CGO
# Stage 2 (runtime) : copie uniquement le binaire dans une image alpine propre
#
# Taille finale ~15 MB grâce au multi-stage et CGO_ENABLED=0.
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1 : BUILD
# Utilise l'image officielle Go 1.22 sur Alpine pour minimiser la taille.
# ---------------------------------------------------------------------------
FROM golang:1.22-alpine AS builder

# Installer les dépendances de compilation nécessaires à lib/pq (cgo disabled
# ici mais on garde git pour les modules privés éventuels)
RUN apk add --no-cache git ca-certificates

# Répertoire de travail dans le conteneur de build
WORKDIR /app

# Copier les fichiers de dépendances en premier pour tirer parti du cache Docker.
# Les layers go.mod/go.sum ne seront recalculés que si ces fichiers changent.
COPY go.mod go.sum ./

# Télécharger toutes les dépendances Go déclarées dans go.mod
RUN go mod download

# Copier l'intégralité du code source
COPY . .

# Compiler le binaire en mode statique (CGO_ENABLED=0) pour éviter les
# dépendances sur les bibliothèques C de l'hôte.
# -ldflags="-s -w" supprime les symboles de debug pour réduire la taille.
# -trimpath supprime les chemins absolus du binaire (sécurité + reproductibilité).
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /app/la-chouette-engine \
    ./cmd/main.go

# ---------------------------------------------------------------------------
# Stage 2 : RUNTIME
# Image minimale alpine:3.19 — seul le binaire compilé est copié.
# ---------------------------------------------------------------------------
FROM alpine:3.19

# Métadonnées de l'image
LABEL org.opencontainers.image.title="la-chouette-engine"
LABEL org.opencontainers.image.description="Moteur de résolution gaussienne en temps réel — La Chouette"
LABEL org.opencontainers.image.version="1.0.0"

# ca-certificates est nécessaire pour les connexions TLS éventuelles
# tzdata permet de gérer les fuseaux horaires dans les timestamps
RUN apk add --no-cache ca-certificates tzdata

# Utilisateur non-root pour la sécurité du conteneur en production
RUN addgroup -S chouette && adduser -S chouette -G chouette

# Répertoire de travail du service
WORKDIR /app

# Copier uniquement le binaire compilé depuis le stage builder
COPY --from=builder /app/la-chouette-engine .

# Assigner la propriété à l'utilisateur non-root
RUN chown chouette:chouette /app/la-chouette-engine

# Basculer sur l'utilisateur non-root
USER chouette

# Le binaire écoute Kafka et PostgreSQL — pas de port exposé côté service
# (il est consommateur/producteur, pas serveur HTTP)

# Point d'entrée : lancement direct du moteur
ENTRYPOINT ["/app/la-chouette-engine"]
