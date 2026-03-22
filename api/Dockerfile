# =============================================================================
# La Chouette — Dockerfile API (.NET 8)
# Build multi-stage : compilation → image de production légère
#
# Étapes :
#   1. builder  → SDK .NET 8 complet pour la compilation et la publication
#   2. runtime  → Image ASP.NET runtime seule (bien plus légère que le SDK)
# =============================================================================

# ── Étape 1 : Compilation ─────────────────────────────────────────────────────
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS builder

WORKDIR /app

# Copie des fichiers de projet en premier pour profiter du cache Docker
# (si seul Program.cs change, le restore n'est pas rejoué)
COPY *.csproj ./
RUN dotnet restore --no-cache

# Copie de tout le code source
COPY . .

# Publication en mode Release — optimisée, auto-contenue dans /app/publish
RUN dotnet publish -c Release -o /app/publish

# ── Étape 2 : Image de production ─────────────────────────────────────────────
FROM mcr.microsoft.com/dotnet/aspnet:10.0

WORKDIR /app

# Copie uniquement les artefacts compilés depuis l'étape builder
COPY --from=builder /app/publish .

# Port exposé par l'API (configuré via ASPNETCORE_URLS)
EXPOSE 8080

# ASP.NET Core écoute sur toutes les interfaces sur le port 8080
ENV ASPNETCORE_URLS=http://+:8080

# Désactivation de la redirection HTTPS (inutile en réseau Docker interne)
ENV ASPNETCORE_ENVIRONMENT=Production

# Point d'entrée — lance le binaire publié
ENTRYPOINT ["dotnet", "api.dll"]
