// source-alpha — Producteur Kafka pour les équations assignées à la source "alpha".
//
// Comportement :
//  1. Attend que PostgreSQL soit disponible (retry toutes les 5s, max 30 essais).
//  2. Attend que le problem-generator ait terminé et que des équations soient disponibles.
//  3. Boucle : lit la prochaine équation non dispatched, publie sur le topic "equations",
//     marque l'équation comme dispatched dans PostgreSQL, attend l'intervalle configuré.
//  4. Quand toutes les équations sont publiées, log "Source ALPHA terminée" et exit proprement.
//
// Variables d'environnement :
//   - KAFKA_BROKERS            : adresse(s) du broker Kafka (défaut: kafka:9092)
//   - POSTGRES_DSN             : DSN PostgreSQL complet
//   - SOURCE_ALPHA_INTERVAL_MS : intervalle entre publications en ms (défaut: 3000)
//   - DEMO_MODE                : si "true", divise l'intervalle par 3
//   - LOG_LEVEL                : DEBUG | INFO | WARN | ERROR (défaut: INFO)
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	kafka "github.com/segmentio/kafka-go"
)

// ─────────────────────────────────────────────────────────────────────────────
// Structures de données
// ─────────────────────────────────────────────────────────────────────────────

// EquationRow représente une ligne lue depuis problem_equations.
type EquationRow struct {
	EquationID     string             // identifiant unique, ex: "eq_001"
	Coefficients   map[string]float64 // {"x1": 2.0, "x3": -1.5, ...}
	Constant       float64            // membre droit de l'équation
	DispatchOrder  int                // ordre de publication
}

// KafkaMessage est la structure JSON publiée sur le topic "equations".
type KafkaMessage struct {
	Source       string             `json:"source"`
	EquationID   string             `json:"equation_id"`
	Coefficients map[string]float64 `json:"coefficients"`
	Constant     float64            `json:"constant"`
	Timestamp    string             `json:"timestamp"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Configuration
// ─────────────────────────────────────────────────────────────────────────────

// Config regroupe tous les paramètres de configuration de la source.
type Config struct {
	KafkaBrokers  string        // adresses broker séparées par des virgules
	PostgresDSN   string        // DSN PostgreSQL
	IntervalMs    int64         // intervalle de base entre publications (ms)
	DemoMode      bool          // mode démo : divise l'intervalle par 3
	LogLevel      string        // niveau de log
	SourceName    string        // "alpha" — nom de la source dans les messages Kafka
	KafkaTopic    string        // topic cible, "equations"
}

// loadConfig lit les variables d'environnement et retourne la configuration.
func loadConfig() Config {
	intervalMs := int64(3000) // défaut 3 secondes
	if v := os.Getenv("SOURCE_ALPHA_INTERVAL_MS"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			intervalMs = parsed
		}
	}

	demoMode := strings.ToLower(os.Getenv("DEMO_MODE")) == "true"
	if demoMode {
		// En mode démo, on accélère l'expérience : intervalle divisé par 3
		intervalMs = intervalMs / 3
	}

	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		brokers = "kafka:9092"
	}

	return Config{
		KafkaBrokers: brokers,
		PostgresDSN:  os.Getenv("POSTGRES_DSN"),
		IntervalMs:   intervalMs,
		DemoMode:     demoMode,
		LogLevel:     os.Getenv("LOG_LEVEL"),
		SourceName:   "alpha",
		KafkaTopic:   "equations",
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Logging — couleur BLEUE pour [ALPHA]
// ─────────────────────────────────────────────────────────────────────────────

// Codes ANSI pour la couleur bleue et le reset.
const (
	colorBlue  = "\033[34m"
	colorReset = "\033[0m"
)

// setupLogger configure zerolog avec le niveau demandé et une sortie console
// colorisée. Le préfixe [ALPHA] est injecté en bleu dans chaque message.
func setupLogger(level string) {
	// Sortie console avec couleurs et timestamp lisible
	output := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02T15:04:05.000Z",
		// Ajout du préfixe coloré [ALPHA] au formatage du message
		FormatMessage: func(i interface{}) string {
			return fmt.Sprintf("%s[ALPHA]%s %s", colorBlue, colorReset, i)
		},
	}

	// Niveau de log configurable
	lvl := zerolog.InfoLevel
	switch strings.ToUpper(level) {
	case "DEBUG":
		lvl = zerolog.DebugLevel
	case "WARN":
		lvl = zerolog.WarnLevel
	case "ERROR":
		lvl = zerolog.ErrorLevel
	}

	log.Logger = zerolog.New(output).
		Level(lvl).
		With().
		Timestamp().
		Logger()
}

// ─────────────────────────────────────────────────────────────────────────────
// Connexion PostgreSQL — avec retry
// ─────────────────────────────────────────────────────────────────────────────

// connectPostgres tente de se connecter à PostgreSQL toutes les 5 secondes,
// jusqu'à maxAttempts tentatives. Retourne une erreur si toutes échouent.
func connectPostgres(ctx context.Context, dsn string, maxAttempts int) (*sql.DB, error) {
	var db *sql.DB
	var err error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Vérification du contexte annulé (signal SIGTERM/SIGINT reçu)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		log.Info().Msgf("Tentative de connexion PostgreSQL %d/%d...", attempt, maxAttempts)

		db, err = sql.Open("postgres", dsn)
		if err == nil {
			// sql.Open ne valide pas la connexion, on force un ping
			if pingErr := db.PingContext(ctx); pingErr == nil {
				log.Info().Msg("Connexion PostgreSQL établie")
				return db, nil
			} else {
				err = pingErr
				db.Close()
			}
		}

		log.Warn().Err(err).Msgf("PostgreSQL non disponible, nouvelle tentative dans 5s...")

		// Attente interruptible par le contexte
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}

	return nil, fmt.Errorf("impossible de joindre PostgreSQL après %d tentatives : %w", maxAttempts, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Connexion Kafka — avec retry
// ─────────────────────────────────────────────────────────────────────────────

// connectKafka crée un writer Kafka avec retry automatique intégré.
// kafka-go gère nativement la reconnexion, mais on effectue un test préalable.
func connectKafka(ctx context.Context, brokers string, topic string) (*kafka.Writer, error) {
	brokerList := strings.Split(brokers, ",")

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(brokerList...),
		Topic:                  topic,
		Balancer:               &kafka.LeastBytes{},
		// Retry automatique en cas de déconnexion transitoire
		MaxAttempts:            10,
		RequiredAcks:           kafka.RequireOne,
		Async:                  false, // mode synchrone pour garantir la livraison
		AllowAutoTopicCreation: false,
	}

	// Vérification que le broker est joignable via une métadonnée
	maxKafkaAttempts := 20
	var lastErr error
	for attempt := 1; attempt <= maxKafkaAttempts; attempt++ {
		select {
		case <-ctx.Done():
			writer.Close()
			return nil, ctx.Err()
		default:
		}

		// On tente d'écrire un message de test vide pour valider la connexion ;
		// on préfère lire les métadonnées via une connexion directe.
		conn, dialErr := kafka.DialContext(ctx, "tcp", brokerList[0])
		if dialErr == nil {
			conn.Close()
			log.Info().Msg("Connexion Kafka établie")
			return writer, nil
		}
		lastErr = dialErr

		log.Warn().Err(dialErr).Msgf("Kafka non disponible, tentative %d/%d, nouvelle tentative dans 5s...", attempt, maxKafkaAttempts)

		select {
		case <-ctx.Done():
			writer.Close()
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}

	writer.Close()
	return nil, fmt.Errorf("impossible de joindre Kafka après %d tentatives : %w", maxKafkaAttempts, lastErr)
}

// ─────────────────────────────────────────────────────────────────────────────
// Lecture et dispatch des équations
// ─────────────────────────────────────────────────────────────────────────────

// waitForEquations attend que le problem-generator ait inséré des équations
// pour la source "alpha" dans PostgreSQL. Retente toutes les 3 secondes.
func waitForEquations(ctx context.Context, db *sql.DB, source string) (int, error) {
	log.Info().Msg("En attente des équations du problem-generator...")

	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}

		var count int
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM problem_equations WHERE assigned_source = $1`,
			source,
		).Scan(&count)

		if err != nil {
			log.Warn().Err(err).Msg("Erreur lors du comptage des équations, nouvelle tentative dans 3s...")
		} else if count > 0 {
			return count, nil
		} else {
			log.Debug().Msg("Aucune équation disponible encore, nouvelle tentative dans 3s...")
		}

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// fetchNextEquation lit la prochaine équation non dispatched pour la source donnée.
// Retourne nil, nil si toutes les équations sont déjà dispatched.
func fetchNextEquation(ctx context.Context, db *sql.DB, source string) (*EquationRow, error) {
	var eq EquationRow
	var coefficientsJSON []byte

	err := db.QueryRowContext(ctx, `
		SELECT equation_id, coefficients, constant, dispatch_order
		FROM problem_equations
		WHERE assigned_source = $1 AND dispatched = FALSE
		ORDER BY dispatch_order ASC
		LIMIT 1
	`, source).Scan(
		&eq.EquationID,
		&coefficientsJSON,
		&eq.Constant,
		&eq.DispatchOrder,
	)

	if err == sql.ErrNoRows {
		// Toutes les équations ont été dispatched
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("erreur lecture équation : %w", err)
	}

	// Désérialisation des coefficients depuis le JSONB PostgreSQL
	if err := json.Unmarshal(coefficientsJSON, &eq.Coefficients); err != nil {
		return nil, fmt.Errorf("erreur désérialisation coefficients : %w", err)
	}

	return &eq, nil
}

// markDispatched marque une équation comme dispatched dans PostgreSQL.
func markDispatched(ctx context.Context, db *sql.DB, equationID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE problem_equations SET dispatched = TRUE WHERE equation_id = $1`,
		equationID,
	)
	if err != nil {
		return fmt.Errorf("erreur marquage dispatched pour %s : %w", equationID, err)
	}
	return nil
}

// publishEquation sérialise l'équation en JSON et la publie sur le topic Kafka.
// Retry intégré dans le writer Kafka (MaxAttempts = 10).
func publishEquation(ctx context.Context, writer *kafka.Writer, eq *EquationRow, source string) error {
	msg := KafkaMessage{
		Source:       source,
		EquationID:   eq.EquationID,
		Coefficients: eq.Coefficients,
		Constant:     eq.Constant,
		Timestamp:    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("erreur sérialisation message : %w", err)
	}

	kafkaMsg := kafka.Message{
		Key:   []byte(eq.EquationID), // clé = equation_id pour le partitionnement
		Value: payload,
	}

	if err := writer.WriteMessages(ctx, kafkaMsg); err != nil {
		return fmt.Errorf("erreur publication Kafka : %w", err)
	}
	return nil
}

// formatEquationLog formate une représentation lisible de l'équation pour les logs.
// Exemple : "2.00*x1 + (-1.50)*x3 = 4.16"
func formatEquationLog(eq *EquationRow) string {
	var terms []string
	for varName, coeff := range eq.Coefficients {
		terms = append(terms, fmt.Sprintf("%.2f*%s", coeff, varName))
	}
	return fmt.Sprintf("%s = %.2f", strings.Join(terms, " + "), eq.Constant)
}

// applyJitter applique un jitter aléatoire de ±10% à la durée de base.
// Cela simule des intervalles légèrement variables, plus réalistes.
func applyJitter(base time.Duration) time.Duration {
	// jitter entre -10% et +10%
	jitterFactor := 0.9 + rand.Float64()*0.2
	return time.Duration(float64(base) * jitterFactor)
}

// ─────────────────────────────────────────────────────────────────────────────
// Point d'entrée
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	// ── Chargement de la configuration ──────────────────────────────────────
	cfg := loadConfig()
	setupLogger(cfg.LogLevel)

	log.Info().
		Str("kafka_brokers", cfg.KafkaBrokers).
		Str("topic", cfg.KafkaTopic).
		Int64("interval_ms", cfg.IntervalMs).
		Bool("demo_mode", cfg.DemoMode).
		Msg("Démarrage source-alpha")

	// ── Context avec gestion du shutdown propre (SIGTERM / SIGINT) ───────────
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// ── Connexion PostgreSQL ─────────────────────────────────────────────────
	db, err := connectPostgres(ctx, cfg.PostgresDSN, 30)
	if err != nil {
		log.Fatal().Err(err).Msg("Connexion PostgreSQL impossible")
	}
	defer db.Close()

	// ── Réinitialisation du flag dispatched ─────────────────────────────────
	// Permet le rejeu complet si le container redémarre sans purger le volume.
	if _, resetErr := db.ExecContext(ctx,
		`UPDATE problem_equations SET dispatched = FALSE WHERE assigned_source = $1`,
		cfg.SourceName,
	); resetErr != nil {
		log.Warn().Err(resetErr).Msg("Impossible de réinitialiser les flags dispatched")
	} else {
		log.Info().Msg("Flags dispatched réinitialisés — rejeu complet des équations")
	}

	// ── Attente du démarrage de l'expérience ────────────────────────────────────
	// L'utilisateur doit cliquer "START" sur le dashboard (http://localhost:3000)
	// avant que les sources ne commencent à publier.
	log.Info().Msgf("%s[ALPHA]%s En attente du démarrage — ouvrez http://localhost:3000 et cliquez START", colorBlue, colorReset)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var started bool
		err := db.QueryRowContext(ctx, `SELECT experiment_started FROM investigation_state WHERE id = 1`).Scan(&started)
		if err != nil {
			log.Warn().Err(err).Msg("Erreur lecture experiment_started, nouvelle tentative dans 2s...")
		} else if started {
			log.Info().Msgf("%s[ALPHA]%s Expérience démarrée ! Publication des équations...", colorBlue, colorReset)
			break
		} else {
			log.Info().Msgf("%s[ALPHA]%s En attente du signal START sur http://localhost:3000 ...", colorBlue, colorReset)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	// ── Connexion Kafka ──────────────────────────────────────────────────────
	writer, err := connectKafka(ctx, cfg.KafkaBrokers, cfg.KafkaTopic)
	if err != nil {
		log.Fatal().Err(err).Msg("Connexion Kafka impossible")
	}
	defer writer.Close()

	// ── Attente des équations (le problem-generator doit avoir terminé) ───────
	totalEquations, err := waitForEquations(ctx, db, cfg.SourceName)
	if err != nil {
		log.Fatal().Err(err).Msg("Interruption pendant l'attente des équations")
	}

	// Pause de 3s avant de commencer, pour laisser l'infrastructure se stabiliser
	log.Info().Msgf("%d équations assignées. Début de la publication dans 3s...", totalEquations)
	select {
	case <-ctx.Done():
		log.Info().Msg("Arrêt demandé avant le démarrage de la boucle principale")
		return
	case <-time.After(3 * time.Second):
	}

	// ── Boucle principale de publication ─────────────────────────────────────
	published := 0
	baseInterval := time.Duration(cfg.IntervalMs) * time.Millisecond

	for {
		// Vérification du signal d'arrêt avant chaque itération
		select {
		case <-ctx.Done():
			log.Info().Msgf("Arrêt propre — %d/%d équations publiées", published, totalEquations)
			return
		default:
		}

		// 1. Lecture de la prochaine équation non dispatched
		eq, err := fetchNextEquation(ctx, db, cfg.SourceName)
		if err != nil {
			log.Error().Err(err).Msg("Erreur lors de la lecture de l'équation suivante")
			// Attente courte avant de reessayer en cas d'erreur transitoire
			time.Sleep(2 * time.Second)
			continue
		}

		// Nil = toutes les équations ont été envoyées
		if eq == nil {
			break
		}

		// 2. Publication sur Kafka
		if err := publishEquation(ctx, writer, eq, cfg.SourceName); err != nil {
			log.Error().Err(err).Str("equation_id", eq.EquationID).Msg("Erreur publication Kafka")
			// On ne marque pas l'équation comme dispatched si la publication a échoué
			time.Sleep(2 * time.Second)
			continue
		}

		// 3. Marquage comme dispatched dans PostgreSQL
		if err := markDispatched(ctx, db, eq.EquationID); err != nil {
			// Log de l'erreur mais on continue : l'équation sera re-publiée
			// au prochain redémarrage (idempotence côté consommateur nécessaire)
			log.Error().Err(err).Str("equation_id", eq.EquationID).Msg("Erreur marquage dispatched")
		}

		published++

		// 4. Calcul de l'intervalle avec jitter ±10%
		interval := applyJitter(baseInterval)

		// 5. Log de la publication avec détails de l'équation
		log.Info().Msgf("→ %s | %s | Prochain envoi dans %.1fs",
			eq.EquationID,
			formatEquationLog(eq),
			interval.Seconds(),
		)

		// 6. Attente interruptible avant la prochaine publication
		select {
		case <-ctx.Done():
			log.Info().Msgf("Arrêt propre — %d/%d équations publiées", published, totalEquations)
			return
		case <-time.After(interval):
		}
	}

	// ── Fin normale : toutes les équations ont été publiées ───────────────────
	log.Info().Msgf("✓ Toutes les équations publiées. Source ALPHA terminée.")
}
