#!/usr/bin/env bash
# =============================================================================
# reset.sh — La Chouette
# Réinitialise l'expérience en cours via l'API REST.
#
# Ce script envoie un POST à /api/experiment/reset, ce qui :
#   - Remet à zéro les compteurs et métriques en mémoire
#   - Vide les tables de résultats dans PostgreSQL
#   - Recommence l'expérience depuis le début (sans supprimer la structure BDD)
#
# Usage : ./scripts/reset.sh
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

# URL de l'API (peut être surchargée via la variable d'environnement API_URL)
API_URL="${API_URL:-http://localhost:8080}"
RESET_ENDPOINT="${API_URL}/api/experiment/reset"

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
  echo -e "${CYAN}${BOLD}╔══════════════════════════════════════════════╗${RESET_COLOR}"
  echo -e "${CYAN}${BOLD}║        La Chouette — Reset expérience        ║${RESET_COLOR}"
  echo -e "${CYAN}${BOLD}╚══════════════════════════════════════════════╝${RESET_COLOR}"
  echo ""
}

print_info() {
  echo -e "${CYAN}[INFO]${RESET_COLOR}  $1"
}

print_warn() {
  echo -e "${YELLOW}[WARN]${RESET_COLOR}  $1"
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
# Affichage de l'en-tête et des informations
# ---------------------------------------------------------------------------

print_header

print_info "Cible : ${RESET_ENDPOINT}"
echo ""
print_warn "Cette action va RÉINITIALISER l'expérience en cours :"
echo "   • Les compteurs et métriques seront remis à zéro"
echo "   • Les résultats persistés seront effacés"
echo "   • L'expérience redémarrera depuis le début"
echo "   • La structure de la base de données est conservée"
echo ""

# ---------------------------------------------------------------------------
# Confirmation interactive
# ---------------------------------------------------------------------------

read -r -p "$(echo -e "${YELLOW}Confirmer le reset ? [o/N] :${RESET_COLOR} ")" confirmation

# Accepte "o", "O", "oui", "OUI", "y", "Y", "yes", "YES"
if [[ ! "$confirmation" =~ ^([oO]|[oO][uU][iI]|[yY]|[yY][eE][sS])$ ]]; then
  echo ""
  print_info "Reset annulé. Aucune modification effectuée."
  echo ""
  exit 0
fi

echo ""

# ---------------------------------------------------------------------------
# Appel à l'API
# ---------------------------------------------------------------------------

print_info "Envoi de la requête de reset à l'API..."
echo ""

# curl options :
#   -s         : mode silencieux (pas de barre de progression)
#   -S         : affiche les erreurs malgré -s
#   -X POST    : méthode HTTP
#   -w         : affiche le code HTTP en fin de sortie
#   -o         : redirige le corps de la réponse vers une variable
#   --max-time : timeout de 30 secondes

HTTP_RESPONSE=$(curl -sS \
  -X POST \
  -H "Content-Type: application/json" \
  --max-time 30 \
  -w "\n%{http_code}" \
  "${RESET_ENDPOINT}" 2>&1) || {
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
  print_ok "Reset effectué avec succès (HTTP ${HTTP_CODE})."
  if [[ -n "$HTTP_BODY" ]]; then
    echo ""
    print_info "Réponse de l'API :"
    echo "   ${HTTP_BODY}"
  fi
  echo ""
  print_ok "L'expérience a redémarré. Consultez le dashboard : http://localhost:3000"
  echo ""
else
  print_error "Le reset a échoué (HTTP ${HTTP_CODE})."
  if [[ -n "$HTTP_BODY" ]]; then
    echo ""
    print_error "Détail de l'erreur :"
    echo "   ${HTTP_BODY}"
  fi
  echo ""
  exit 1
fi
