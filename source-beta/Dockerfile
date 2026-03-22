# =============================================================================
# Dockerfile — source-beta
# Build multi-stage : compilation avec SDK, exécution avec runtime allégé
# =============================================================================

# ---------------------------------------------------------------------------
# Étape 1 : BUILD — compilation et publication de l'application
# On utilise l'image SDK complète qui contient dotnet build, restore, publish
# ---------------------------------------------------------------------------
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS builder

WORKDIR /app

# Copier uniquement le .csproj en premier pour bénéficier du cache Docker :
# si les dépendances NuGet n'ont pas changé, dotnet restore est mis en cache
COPY *.csproj ./
RUN dotnet restore

# Copier le reste du code source
COPY . .

# Publication en mode Release : optimisation activée, pas de symboles de debug
RUN dotnet publish -c Release -o /app/publish

# ---------------------------------------------------------------------------
# Étape 2 : RUNTIME — image allégée sans le SDK de compilation
# mcr.microsoft.com/dotnet/runtime pèse ~200 Mo contre ~800 Mo pour le SDK
# ---------------------------------------------------------------------------
FROM mcr.microsoft.com/dotnet/runtime:10.0

WORKDIR /app

# Copier uniquement les artefacts compilés depuis l'étape builder
COPY --from=builder /app/publish .

# Point d'entrée : lancer directement la DLL compilée
ENTRYPOINT ["dotnet", "source-beta.dll"]
