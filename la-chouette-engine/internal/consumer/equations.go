// Package consumer implémente les consommateurs Kafka du moteur La Chouette.
//
// Deux consumers sont fournis :
//   - EquationsConsumer : écoute le topic "equations", orchestre la résolution
//     et publie les résultats sur le topic "results".
//   - NoiseConsumer : écoute le topic "noise", logue et persiste le bruit.
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"lachouette/la-chouette-engine/internal/solver"
	"lachouette/la-chouette-engine/internal/store"
)

// cyanColor est le code ANSI couleur cyan pour les logs [CHOUETTE].
const cyanColor = "\033[36m"

// resetColor remet la couleur du terminal à sa valeur par défaut.
const resetColor = "\033[0m"

// boldColor active le gras ANSI.
const boldColor = "\033[1m"

// EquationMessage représente le format JSON attendu sur le topic "equations".
// Chaque message correspond à une équation linéaire à intégrer dans le solveur.
type EquationMessage struct {
	// Source identifie la source émettrice ("alpha", "beta", "gamma").
	Source string `json:"source"`

	// EquationID est l'identifiant unique de l'équation (ex: "eq_023").
	EquationID string `json:"equation_id"`

	// Coefficients est la map nom→valeur des coefficients non-nuls.
	// Ex: {"x1": 2.0, "x3": -1.5, "x47": 3.0}
	Coefficients map[string]float64 `json:"coefficients"`

	// Constant est le membre droit de l'équation.
	Constant float64 `json:"constant"`

	// Timestamp est la date d'émission du message par la source.
	Timestamp time.Time `json:"timestamp"`
}

// ResultMessage représente le format JSON publié sur le topic "results".
// Deux types de messages sont produits :
//   - "VARIABLE_RESOLVED" : une variable vient d'être résolue.
//   - "PROGRESS_UPDATE"   : mise à jour périodique de la progression.
type ResultMessage struct {
	// Type indique la nature du message ("VARIABLE_RESOLVED" ou "PROGRESS_UPDATE").
	Type string `json:"type"`

	// VariableName est le nom de la variable résolue (présent si Type="VARIABLE_RESOLVED").
	VariableName string `json:"variable_name,omitempty"`

	// Value est la valeur calculée (présent si Type="VARIABLE_RESOLVED").
	Value float64 `json:"value,omitempty"`

	// EquationsReceived est le nombre total d'équations reçues au moment de l'émission.
	EquationsReceived int `json:"equations_received"`

	// VariablesResolved est le nombre de variables résolues au moment de l'émission.
	VariablesResolved int `json:"variables_resolved"`

	// VariablesTotal est le nombre total d'inconnues dans le système.
	VariablesTotal int `json:"variables_total"`

	// CompletionPercentage est le pourcentage de variables résolues.
	CompletionPercentage float64 `json:"completion_percentage"`

	// Status est le statut courant du solveur.
	Status string `json:"status"`

	// StatusMessage est le message lisible décrivant l'état.
	StatusMessage string `json:"status_message"`

	// Timestamp est la date d'émission du message de résultat.
	Timestamp time.Time `json:"timestamp"`
}

// EquationsConsumer est le consumer Kafka pour le topic "equations".
// Il orchestre la résolution gaussienne et la persistance des résultats.
type EquationsConsumer struct {
	// reader est le reader Kafka configuré pour le topic "equations".
	reader *kafkago.Reader

	// producer est le writer Kafka pour publier sur le topic "results".
	producer *kafkago.Writer

	// solver est le moteur de résolution gaussienne partagé.
	solver *solver.GaussianSolver

	// repo est le dépôt PostgreSQL pour la persistance.
	repo *store.Repository

	// previousStatus mémorise le dernier statut pour détecter les transitions.
	previousStatus solver.Status
}

// NewEquationsConsumer crée un EquationsConsumer prêt à consommer le topic "equations"
// et à publier sur le topic "results".
//
// brokers est l'adresse du broker Kafka (ex: "kafka:9092").
// topic est le nom du topic à consommer ("equations").
// groupID est l'identifiant du consumer group ("chouette-eq-group").
// gSolver est l'instance du solveur gaussien partagé.
// repo est l'instance du dépôt PostgreSQL.
func NewEquationsConsumer(brokers, topic, groupID string, gSolver *solver.GaussianSolver, repo *store.Repository) *EquationsConsumer {
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:        []string{brokers},
		Topic:          topic,
		GroupID:        groupID,
		MinBytes:       1,
		MaxBytes:       10e6,
		CommitInterval: time.Second,
		// StartOffset: kafkago.FirstOffset permet de rejouer depuis le début si le groupe est nouveau
		StartOffset: kafkago.FirstOffset,
	})

	producer := &kafkago.Writer{
		Addr:         kafkago.TCP(brokers),
		Topic:        "results",
		Balancer:     &kafkago.LeastBytes{},
		RequiredAcks: kafkago.RequireOne,
		// Async pour ne pas bloquer le traitement des équations
		Async: false,
	}

	return &EquationsConsumer{
		reader:         reader,
		producer:       producer,
		solver:         gSolver,
		repo:           repo,
		previousStatus: solver.StatusCollecting,
	}
}

// Run démarre la boucle principale de consommation du topic "equations".
//
// La boucle tourne indéfiniment jusqu'à l'annulation du contexte (SIGTERM/SIGINT).
// Pour chaque message reçu :
//  1. Désérialisation JSON
//  2. Log [CHOUETTE] de réception
//  3. Persistance dans equations_received
//  4. Appel au solveur gaussien
//  5. Pour chaque variable résolue : log + persistance + publication sur results
//  6. Publication d'un PROGRESS_UPDATE sur results
//  7. Mise à jour de investigation_state
func (c *EquationsConsumer) Run(ctx context.Context) error {
	defer c.reader.Close()
	defer c.producer.Close()

	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "%s%s[CHOUETTE]%s %s\n", boldColor, cyanColor, resetColor, fmt.Sprintf(format, args...))
	}

	logf("Consumer equations démarré | topic: equations | group: chouette-eq-group")

	for {
		// Lecture bloquante avec propagation du contexte pour l'arrêt propre
		msg, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// Contexte annulé → arrêt normal
				logf("Consumer equations arrêté (contexte annulé)")
				return nil
			}
			logf("Erreur lecture Kafka equations: %v — nouvelle tentative...", err)
			time.Sleep(time.Second)
			continue
		}

		// -------------------------------------------------------------------
		// 1. Désérialisation du message JSON
		// -------------------------------------------------------------------
		var eqMsg EquationMessage
		if err := json.Unmarshal(msg.Value, &eqMsg); err != nil {
			logf("Message invalide (JSON): %v — ignoré", err)
			continue
		}

		numVars := len(eqMsg.Coefficients)

		// -------------------------------------------------------------------
		// 2. Log de réception détaillé
		// -------------------------------------------------------------------
		currentStatus := c.solver.GetStatus()
		logf("← %s reçu | source:%s | %d variables impliquées | Équations: %d",
			eqMsg.EquationID,
			eqMsg.Source,
			numVars,
			currentStatus.EquationsReceived+1,
		)

		// -------------------------------------------------------------------
		// 3. Persistance de l'équation reçue dans PostgreSQL
		// -------------------------------------------------------------------
		if err := c.repo.SaveEquationReceived(eqMsg.EquationID, eqMsg.Source, eqMsg.Coefficients, eqMsg.Constant); err != nil {
			logf("Erreur persistance equation_received: %v", err)
			// Non-fatal : on continue le traitement même si la persistance échoue
		}

		// -------------------------------------------------------------------
		// 4. Résolution gaussienne
		// -------------------------------------------------------------------
		newlyResolved := c.solver.AddEquation(eqMsg.Coefficients, eqMsg.Constant)

		// Statut après résolution
		statusAfter := c.solver.GetStatus()

		// -------------------------------------------------------------------
		// 5. Traitement des variables nouvellement résolues
		// -------------------------------------------------------------------
		if len(newlyResolved) == 0 {
			logf("Analyse: aucune variable résoluble avec %d équation(s) | Statut: %s",
				statusAfter.EquationsReceived, statusAfter.Status)
		}

		for _, rv := range newlyResolved {
			// Log avec erreur absolue
			if rv.HasTrueValue {
				logf("Variable %s = %.4f ✓ | Erreur: %.4f | Équations: %d | Résolues: %d/%d",
					rv.Name, rv.Value, rv.AbsoluteError,
					statusAfter.EquationsReceived,
					statusAfter.VariablesResolved,
					statusAfter.VariablesTotal,
				)
			} else {
				logf("Variable %s = %.4f | Équations: %d | Résolues: %d/%d",
					rv.Name, rv.Value,
					statusAfter.EquationsReceived,
					statusAfter.VariablesResolved,
					statusAfter.VariablesTotal,
				)
			}

			// Persistance de la variable résolue
			if err := c.repo.SaveResolvedVariable(
				rv.Name, rv.Value, rv.TrueValue,
				statusAfter.EquationsReceived, rv.AbsoluteError,
			); err != nil {
				logf("Erreur persistance variable résolue %s: %v", rv.Name, err)
			}

			// Publication sur le topic "results"
			resultMsg := ResultMessage{
				Type:                 "VARIABLE_RESOLVED",
				VariableName:         rv.Name,
				Value:                rv.Value,
				EquationsReceived:    statusAfter.EquationsReceived,
				VariablesResolved:    statusAfter.VariablesResolved,
				VariablesTotal:       statusAfter.VariablesTotal,
				CompletionPercentage: statusAfter.CompletionPct,
				Status:               string(statusAfter.Status),
				StatusMessage:        statusAfter.StatusMessage,
				Timestamp:            time.Now().UTC(),
			}
			if err := c.publishResult(ctx, resultMsg); err != nil {
				logf("Erreur publication VARIABLE_RESOLVED sur results: %v", err)
			}
		}

		// -------------------------------------------------------------------
		// 6. Publication d'un PROGRESS_UPDATE à chaque équation
		// -------------------------------------------------------------------
		progressMsg := ResultMessage{
			Type:                 "PROGRESS_UPDATE",
			EquationsReceived:    statusAfter.EquationsReceived,
			VariablesResolved:    statusAfter.VariablesResolved,
			VariablesTotal:       statusAfter.VariablesTotal,
			CompletionPercentage: statusAfter.CompletionPct,
			Status:               string(statusAfter.Status),
			StatusMessage:        statusAfter.StatusMessage,
			Timestamp:            time.Now().UTC(),
		}
		if err := c.publishResult(ctx, progressMsg); err != nil {
			logf("Erreur publication PROGRESS_UPDATE: %v", err)
		}

		// -------------------------------------------------------------------
		// 7. Mise à jour de investigation_state et log des transitions
		// -------------------------------------------------------------------
		if err := c.repo.UpdateInvestigationState(
			string(statusAfter.Status),
			statusAfter.EquationsReceived,
			statusAfter.VariablesResolved,
			statusAfter.CompletionPct,
		); err != nil {
			logf("Erreur mise à jour investigation_state: %v", err)
		}

		// Log des transitions de statut
		if statusAfter.Status != c.previousStatus {
			logf("Statut: %s → %s", c.previousStatus, statusAfter.Status)
			c.previousStatus = statusAfter.Status
		}

		// Log spécial pour la résolution complète
		if statusAfter.Status == solver.StatusSolved {
			logf("*** SYSTÈME RÉSOLU *** | %s", statusAfter.StatusMessage)
		}
	}
}

// publishResult sérialise un ResultMessage et le publie sur le topic "results" via Kafka.
func (c *EquationsConsumer) publishResult(ctx context.Context, result ResultMessage) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("publishResult: marshal: %w", err)
	}

	return c.producer.WriteMessages(ctx, kafkago.Message{
		Key:   []byte(result.Type),
		Value: data,
	})
}
