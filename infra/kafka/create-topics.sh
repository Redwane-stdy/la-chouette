#!/bin/bash
# =============================================================================
# La Chouette — Création des topics Kafka
# Exécuté par le container kafka-init au démarrage
# =============================================================================

set -e

KAFKA_BROKER="${KAFKA_BROKER:-kafka:9092}"
WAIT_SECONDS="${WAIT_SECONDS:-15}"

echo "[KAFKA-INIT] Attente de ${WAIT_SECONDS}s pour que Kafka soit prêt..."
sleep "${WAIT_SECONDS}"

echo "[KAFKA-INIT] Création du topic: equations"
kafka-topics --bootstrap-server "${KAFKA_BROKER}" \
    --create --if-not-exists \
    --topic equations \
    --partitions 3 \
    --replication-factor 1 \
    --config retention.ms=3600000

echo "[KAFKA-INIT] Création du topic: noise"
kafka-topics --bootstrap-server "${KAFKA_BROKER}" \
    --create --if-not-exists \
    --topic noise \
    --partitions 1 \
    --replication-factor 1 \
    --config retention.ms=3600000

echo "[KAFKA-INIT] Création du topic: results"
kafka-topics --bootstrap-server "${KAFKA_BROKER}" \
    --create --if-not-exists \
    --topic results \
    --partitions 1 \
    --replication-factor 1 \
    --config retention.ms=3600000

echo "[KAFKA-INIT] Topics créés avec succès:"
kafka-topics --bootstrap-server "${KAFKA_BROKER}" --list

echo "[KAFKA-INIT] Terminé."
