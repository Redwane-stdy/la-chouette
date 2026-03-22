#!/bin/bash
# =============================================================================
# La Chouette — Démarrage local (sans Docker)
# Lance chaque service dans un onglet iTerm2/Terminal séparé
# Prérequis : PostgreSQL et Kafka lancés via brew services
# =============================================================================

set -e

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="$PROJECT_DIR/.env"

# --- Chargement des variables d'environnement ---
if [ ! -f "$ENV_FILE" ]; then
  echo "[ERREUR] Fichier .env introuvable. Copie .env.example en .env d'abord."
  exit 1
fi
# set -a exporte automatiquement toutes les variables définies par source
# source gère nativement les commentaires (#) et les lignes vides
set -a
# shellcheck source=/dev/null
source "$ENV_FILE"
set +a

echo "=============================================="
echo "  LA CHOUETTE — Démarrage local"
echo "  DEMO_MODE=$DEMO_MODE"
echo "  KAFKA: $KAFKA_BROKERS"
echo "  POSTGRES: $POSTGRES_DSN"
echo "=============================================="
echo ""

# --- Fonction utilitaire : ouvrir une nouvelle fenêtre Terminal macOS ---
# Écrit la commande dans un script temporaire et l'ouvre avec Terminal.app
# Évite tous les problèmes de guillemets avec AppleScript
open_tab() {
  local title="$1"
  local cmd="$2"

  # Créer un script temporaire pour ce service
  local tmpscript
  tmpscript=$(mktemp /tmp/lachouette_XXXXXX)

  # Écrire le contenu du script
  cat > "$tmpscript" <<SCRIPT
#!/bin/bash
# Titre de la fenêtre Terminal
echo -ne "\\033]0;${title}\\007"
# Charger les variables d'environnement
set -a; source "${ENV_FILE}"; set +a
# Exécuter la commande du service
${cmd}
# Pause à la fin pour garder la fenêtre ouverte
echo ""
echo "[${title}] Processus terminé. Appuie sur Entrée pour fermer."
read
SCRIPT

  chmod +x "$tmpscript"
  # Ouvrir le script dans une nouvelle fenêtre Terminal
  open -a Terminal "$tmpscript"
  sleep 0.8
}

# --- 1. Problem Generator (Go) — s'exécute une fois ---
echo "[START] Lancement du problem-generator..."
cd "$PROJECT_DIR/problem-generator"
go mod tidy 2>/dev/null
go run ./cmd/main.go
echo "[OK] Problem-generator terminé."
echo ""

# --- 2. La Chouette Engine (Go) ---
echo "[START] Lancement de la-chouette-engine..."
open_tab "CHOUETTE-ENGINE" "cd '$PROJECT_DIR/la-chouette-engine' && go mod tidy 2>/dev/null && go run ./cmd/main.go"
sleep 2

# --- 3. API (C# .NET) ---
echo "[START] Lancement de l'API..."
open_tab "API" "cd '$PROJECT_DIR/api' && dotnet run"
sleep 2

# --- 4. Source Alpha (Go) ---
echo "[START] Lancement de source-alpha..."
open_tab "SOURCE-ALPHA" "cd '$PROJECT_DIR/source-alpha' && go mod tidy 2>/dev/null && go run main.go"

# --- 5. Source Beta (C# .NET) ---
echo "[START] Lancement de source-beta..."
open_tab "SOURCE-BETA" "cd '$PROJECT_DIR/source-beta' && dotnet run"

# --- 6. Source Gamma (Go) ---
echo "[START] Lancement de source-gamma..."
open_tab "SOURCE-GAMMA" "cd '$PROJECT_DIR/source-gamma' && go mod tidy 2>/dev/null && go run main.go"

# --- 7. Source Noise (Go) ---
echo "[START] Lancement de source-noise..."
open_tab "SOURCE-NOISE" "cd '$PROJECT_DIR/source-noise' && go mod tidy 2>/dev/null && go run main.go"

# --- 8. Dashboard (servir les fichiers statiques avec Python) ---
echo "[START] Lancement du dashboard..."
open_tab "DASHBOARD" "cd '$PROJECT_DIR/dashboard' && python3 -m http.server 3000"

echo ""
echo "=============================================="
echo "  Tous les services sont lancés !"
echo "  Dashboard  → http://localhost:3000"
echo "  API        → http://localhost:8080/api/status"
echo "=============================================="
