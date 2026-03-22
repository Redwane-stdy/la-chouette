// noise.go — Consumer Kafka pour le topic "noise".
//
// Le NoiseConsumer écoute le topic "noise" et traite les messages parasites
// injectés par source-noise. Il ne participe pas à la résolution mathématique
// mais logue le bruit (en gris pour le différencier des logs [CHOUETTE]) et
// le persiste dans la table noise_log de PostgreSQL pour affichage dans le
// dashboard.
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"lachouette/la-chouette-engine/internal/store"
)

// grayColor est le code ANSI gris clair pour les logs [NOISE].
// Le gris permet de différencier visuellement le bruit des événements du moteur.
const grayColor = "\033[90m"

// NoiseMessage représente le format JSON attendu sur le topic "noise".
// Ce sont des messages parasites simulant un flux d'actualités médiatiques.
type NoiseMessage struct {
	// Source identifie la chaîne d'information émettrice (ex: "reuters", "bfm").
	Source string `json:"source"`

	// Headline est le titre de l'actualité simulée.
	Headline string `json:"headline"`

	// Timestamp est la date d'émission du message.
	Timestamp time.Time `json:"timestamp"`
}

// NoiseConsumer est le consumer Kafka pour le topic "noise".
// Il logue les messages parasites en gris et les persiste dans noise_log.
type NoiseConsumer struct {
	// reader est le reader Kafka configuré pour le topic "noise".
	reader *kafkago.Reader

	// repo est le dépôt PostgreSQL pour la persistance du bruit.
	repo *store.Repository
}

// NewNoiseConsumer crée un NoiseConsumer prêt à consommer le topic "noise".
//
// brokers est l'adresse du broker Kafka (ex: "kafka:9092").
// topic est le nom du topic à consommer ("noise").
// groupID est l'identifiant du consumer group ("chouette-noise-group").
// repo est l'instance du dépôt PostgreSQL.
func NewNoiseConsumer(brokers, topic, groupID string, repo *store.Repository) *NoiseConsumer {
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:        []string{brokers},
		Topic:          topic,
		GroupID:        groupID,
		MinBytes:       1,
		MaxBytes:       1e6,
		CommitInterval: time.Second,
		StartOffset:    kafkago.FirstOffset,
	})

	return &NoiseConsumer{
		reader: reader,
		repo:   repo,
	}
}

// Run démarre la boucle principale de consommation du topic "noise".
//
// La boucle tourne indéfiniment jusqu'à l'annulation du contexte.
// Pour chaque message reçu :
//  1. Désérialisation JSON
//  2. Log [NOISE] en gris (pour différencier visuellement du moteur)
//  3. Persistance dans noise_log (pas de traitement algorithmique)
func (c *NoiseConsumer) Run(ctx context.Context) error {
	defer c.reader.Close()

	logNoise := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "%s[NOISE]%s    %s\n", grayColor, resetColor, fmt.Sprintf(format, args...))
	}

	logNoise("Consumer noise démarré | topic: noise | group: chouette-noise-group")

	for {
		// Lecture bloquante avec propagation du contexte
		msg, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// Contexte annulé → arrêt normal et silencieux
				return nil
			}
			logNoise("Erreur lecture Kafka noise: %v — nouvelle tentative...", err)
			time.Sleep(time.Second)
			continue
		}

		// -------------------------------------------------------------------
		// 1. Désérialisation JSON du message de bruit
		// -------------------------------------------------------------------
		var noiseMsg NoiseMessage
		if err := json.Unmarshal(msg.Value, &noiseMsg); err != nil {
			// En cas d'erreur de parsing, on logue le message brut en gris
			logNoise("Message non-JSON reçu: %s", string(msg.Value))
			continue
		}

		// Normaliser la source avec le préfixe "noise_" si absent
		source := noiseMsg.Source
		if source == "" {
			source = "noise_unknown"
		}

		headline := noiseMsg.Headline
		if headline == "" {
			headline = string(msg.Value)
		}

		// -------------------------------------------------------------------
		// 2. Log [NOISE] en gris — style distinct du moteur [CHOUETTE]
		// -------------------------------------------------------------------
		logNoise("[%s] \"%s\"", source, headline)

		// -------------------------------------------------------------------
		// 3. Persistance dans noise_log (pas de traitement algorithmique)
		// -------------------------------------------------------------------
		if err := c.repo.SaveNoiseMessage(source, headline); err != nil {
			logNoise("Erreur persistance noise_log: %v", err)
			// Non-fatal : le bruit n'est pas critique
		}
	}
}
