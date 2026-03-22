#!/usr/bin/env bash
# =============================================================================
# purge.sh — La Chouette
# Supprime TOUTES les données de l'expérience via l'API REST.
#
# Ce script envoie un DELETE à /api/experiment/purge, ce qui :
#   - Supprime toutes les tables et leur contenu dans PostgreSQL
#   - Efface les offsets Kafka (les topics restent, mais les données sont perdues)
#   - Supprime les métriques et l'état en mémoire
#   - Vide le volume Docker lachouette_data (si l'API le gère)
#
# ATTENTION : cette action est IRRÉVERSIBLE. Aucune restauration possible.
#
# Usage : ./scripts/purge.sh
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

# URL de l'API (peut être surchargée via la variable d'environnement API_URL)
API_URL="${API_URL:-http://localhost:8080}"
PURGE_ENDPOINT="${API_URL}/api/experiment/purge"

# Couleurs pour l'affichage terminal
RED='\033[0;31m'
YELLOW='\033[1;33m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET_COLOR='\033[0m'

# ---------------------------------------------------------------------------
# Fonctions utilitaires
# ---------------------------------------------------------------------------

print_header() {
  echo ""
  echo -e "${RED}${BOLD}╔══════════════════════════════════════════════╗${RESET_COLOR}"
  echo -e "${RED}${BOLD}║     La Chouette — PURGE IRRÉVERSIBLE ⚠       ║${RESET_COLOR}"
  echo -e "${RED}${BOLD}╚══════════════════════════════════════════════╝${RESET_COLOR}"
  echo ""
}

print_info() {
  echo -e "${CYAN}[INFO]${RESET_COLOR}  $1"
}

print_warn() {
  echo -e "${YELLOW}[WARN]${RESET_COLOR}  $1"
}

print_danger() {
  echo -e "${RED}${BOLD}[!!!]${RESET_COLOR}   $1"
}

print_ok() {
  echo -e "${GREEN}[OK]${RESET_COLOR}    $1"
}

print_error() {
  echo -e "${RED}[ERROR]${RESET_COLOR} $1" >&2
}

# ---------------------------------------------------------------------------
# Vérification des dépendances
# ---------------------------------------------------------------------------

if ! command -v curl &>/dev/null; then
  print_error "curl n'est pas installé. Installez-le et relancez le script."
  exit 1
fi

# ---------------------------------------------------------------------------
# Affichage de l'en-tête et avertissement renforcé
# ---------------------------------------------------------------------------

print_header

print_danger "ATTENTION — OPÉRATION IRRÉVERSIBLE"
echo ""
print_warn "Cette action va SUPPRIMER DÉFINITIVEMENT :"
echo "   • Toutes les données de la base de données PostgreSQL"
echo "   • Tous les résultats et métriques de l'expérience"
echo "   • L'historique complet des événements traités"
echo "   • Les offsets des consumers Kafka"
echo ""
print_warn "Cible : ${PURGE_ENDPOINT}"
echo ""
print_danger "IL N'EXISTE AUCUN MOYEN DE RÉCUPÉRER CES DONNÉES APRÈS LA PURGE."
echo ""

# ---------------------------------------------------------------------------
# Première confirmation
# ---------------------------------------------------------------------------

read -r -p "$(echo -e "${YELLOW}Êtes-vous sûr de vouloir purger toutes les données ? [o/N] :${RESET_COLOR} ")" confirmation_1

if [[ ! "$confirmation_1" =~ ^([oO]|[oO][uU][iI]|[yY]|[yY][eE][sS])$ ]]; then
  echo ""
  print_info "Purge annulée. Aucune modification effectuée."
  echo ""
  exit 0
fi

echo ""

# ---------------------------------------------------------------------------
# Deuxième confirmation — saisie manuelle du mot-clé
# ---------------------------------------------------------------------------

print_danger "DEUXIÈME CONFIRMATION REQUISE"
echo ""
print_warn "Pour confirmer définitivement, tapez exactement le mot : ${BOLD}PURGE${RESET_COLOR}"
echo ""
read -r -p "$(echo -e "${RED}${BOLD}Tapez PURGE pour confirmer :${RESET_COLOR} ")" confirmation_2

if [[ "$confirmation_2" != "PURGE" ]]; then
  echo ""
  print_info "Confirmation incorrecte (\"${confirmation_2}\" ≠ \"PURGE\")."
  print_info "Purge annulée. Aucune modification effectuée."
  echo ""
  exit 0
fi

echo ""

# ---------------------------------------------------------------------------
# Appel à l'API
# ---------------------------------------------------------------------------

print_info "Envoi de la requête de purge à l'API..."
echo ""

# curl options :
#   -s         : mode silencieux (pas de barre de progression)
#   -S         : affiche les erreurs malgré -s
#   -X DELETE  : méthode HTTP DELETE
#   -w         : affiche le code HTTP en fin de sortie
#   --max-time : timeout de 60 secondes (la purge peut prendre du temps)

HTTP_RESPONSE=$(curl -sS \
  -X DELETE \
  -H "Content-Type: application/json" \
  --max-time 60 \
  -w "\n%{http_code}" \
  "${PURGE_ENDPOINT}" 2>&1) || {
    print_error "Impossible de joindre l'API à ${API_URL}."
    print_error "Vérifiez que le stack est bien démarré (docker compose up)."
    exit 1
  }

# Séparation du corps et du code HTTP (dernière ligne)
HTTP_BODY=$(echo "$HTTP_RESPONSE" | head -n -1)
HTTP_CODE=$(echo "$HTTP_RESPONSE" | tail -n 1)

# ---------------------------------------------------------------------------
# Analyse de la réponse
# ---------------------------------------------------------------------------

if [[ "$HTTP_CODE" =~ ^2 ]]; then
  print_ok "Purge effectuée avec succès (HTTP ${HTTP_CODE})."
  if [[ -n "$HTTP_BODY" ]]; then
    echo ""
    print_info "Réponse de l'API :"
    echo "   ${HTTP_BODY}"
  fi
  echo ""
  print_info "Toutes les données ont été supprimées."
  print_info "Pour relancer une expérience propre : docker compose down && docker compose up --build"
  echo ""
else
  print_error "La purge a échoué (HTTP ${HTTP_CODE})."
  if [[ -n "$HTTP_BODY" ]]; then
    echo ""
    print_error "Détail de l'erreur :"
    echo "   ${HTTP_BODY}"
  fi
  echo ""
  exit 1
fi
