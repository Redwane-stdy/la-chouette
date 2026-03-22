// Package main est le point d'entrée du moteur La Chouette.
//
// Il initialise la connexion PostgreSQL, charge les valeurs vraies des variables
// depuis la base de données (générées par problem-generator), instancie le solveur
// gaussien, puis lance les goroutines de consommation Kafka pour les topics
// "equations" et "noise". Le programme se termine proprement sur SIGTERM ou SIGINT.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"lachouette/la-chouette-engine/internal/consumer"
	"lachouette/la-chouette-engine/internal/solver"
	"lachouette/la-chouette-engine/internal/store"
)

// cyanColor est le code ANSI pour la couleur cyan utilisée dans les logs [CHOUETTE].
const cyanColor = "\033[36m"

// resetColor remet la couleur du terminal à sa valeur par défaut.
const resetColor = "\033[0m"

// boldColor active le gras dans le terminal.
const boldColor = "\033[1m"

// envOrDefault retourne la valeur d'une variable d'environnement ou une valeur par défaut.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// chouetteWriter est un writer personnalisé qui ajoute le préfixe cyan [CHOUETTE]
// à chaque ligne de log du moteur principal.
type chouetteWriter struct{}

// Write implémente io.Writer pour chouetteWriter.
// Chaque message est préfixé avec le tag coloré [CHOUETTE].
func (w chouetteWriter) Write(p []byte) (n int, err error) {
	_, err = fmt.Fprintf(os.Stderr, "%s%s[CHOUETTE]%s %s", boldColor, cyanColor, resetColor, string(p))
	return len(p), err
}

func main() {
	// -----------------------------------------------------------------------
	// Configuration du logger zerolog avec sortie colorée CYAN pour [CHOUETTE]
	// -----------------------------------------------------------------------
	logLevel := zerolog.InfoLevel
	switch envOrDefault("LOG_LEVEL", "INFO") {
	case "DEBUG":
		logLevel = zerolog.DebugLevel
	case "WARN":
		logLevel = zerolog.WarnLevel
	case "ERROR":
		logLevel = zerolog.ErrorLevel
	}
	zerolog.SetGlobalLevel(logLevel)

	// Writer console avec couleur et timestamp lisible
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
		NoColor:    false,
	}
	log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

	// -----------------------------------------------------------------------
	// Lecture des variables d'environnement
	// -----------------------------------------------------------------------
	kafkaBrokers := envOrDefault("KAFKA_BROKERS", "kafka:9092")
	postgresDSN := envOrDefault("POSTGRES_DSN", "postgres://lachouette:secret@postgres:5432/lachouette?sslmode=disable")
	numUnknownsStr := envOrDefault("NUM_UNKNOWNS", "50")
	demoMode := envOrDefault("DEMO_MODE", "false") == "true"

	numUnknowns, err := strconv.Atoi(numUnknownsStr)
	if err != nil || numUnknowns <= 0 {
		numUnknowns = 50
	}

	// En mode démonstration, on réduit le nombre d'inconnues
	if demoMode {
		log.Info().Msgf("%s%s[CHOUETTE]%s Mode DÉMO activé — 15 inconnues, intervalles réduits", boldColor, cyanColor, resetColor)
		if numUnknowns == 50 {
			numUnknowns = 15
		}
	}

	// -----------------------------------------------------------------------
	// Connexion PostgreSQL avec tentatives de reconnexion
	// -----------------------------------------------------------------------
	var db *sql.DB
	for attempt := 1; attempt <= 10; attempt++ {
		db, err = sql.Open("postgres", postgresDSN)
		if err != nil {
			log.Error().Err(err).Msgf("%s%s[CHOUETTE]%s Erreur ouverture PostgreSQL (tentative %d/10)", boldColor, cyanColor, resetColor, attempt)
			time.Sleep(3 * time.Second)
			continue
		}
		if pingErr := db.Ping(); pingErr != nil {
			log.Warn().Err(pingErr).Msgf("%s%s[CHOUETTE]%s PostgreSQL pas encore prêt (tentative %d/10)...", boldColor, cyanColor, resetColor, attempt)
			db.Close()
			time.Sleep(3 * time.Second)
			continue
		}
		break
	}
	if db == nil {
		log.Fatal().Msg("Impossible de se connecter à PostgreSQL après 10 tentatives")
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	log.Info().Msgf("%s%s[CHOUETTE]%s Connexion PostgreSQL établie", boldColor, cyanColor, resetColor)

	// -----------------------------------------------------------------------
	// Initialisation du store PostgreSQL
	// -----------------------------------------------------------------------
	repo := store.NewRepository(db)

	// -----------------------------------------------------------------------
	// Chargement des valeurs vraies depuis problem_variables
	// Ces valeurs sont générées par problem-generator et servent uniquement
	// à calculer l'erreur absolue lors de la résolution (vérification).
	// -----------------------------------------------------------------------
	trueValues, err := repo.LoadTrueValues()
	if err != nil {
		// Ce n'est pas fatal : problem-generator n'a peut-être pas encore fini.
		// On attend et réessaie.
		log.Warn().Err(err).Msgf("%s%s[CHOUETTE]%s Valeurs vraies non disponibles — attente de problem-generator...", boldColor, cyanColor, resetColor)
		for attempt := 1; attempt <= 20; attempt++ {
			time.Sleep(3 * time.Second)
			trueValues, err = repo.LoadTrueValues()
			if err == nil && len(trueValues) > 0 {
				break
			}
			log.Warn().Msgf("%s%s[CHOUETTE]%s Tentative %d/20 — problem_variables encore vide", boldColor, cyanColor, resetColor, attempt)
		}
	}

	if len(trueValues) == 0 {
		log.Warn().Msgf("%s%s[CHOUETTE]%s Aucune valeur vraie chargée — l'erreur absolue ne sera pas calculée", boldColor, cyanColor, resetColor)
	} else {
		log.Info().Msgf("%s%s[CHOUETTE]%s %d valeurs vraies chargées depuis problem_variables", boldColor, cyanColor, resetColor, len(trueValues))
	}

	// -----------------------------------------------------------------------
	// Mise à jour de variables_total dans investigation_state
	// -----------------------------------------------------------------------
	if _, updateErr := db.Exec(`UPDATE investigation_state SET variables_total = $1 WHERE id = 1`, numUnknowns); updateErr != nil {
		log.Warn().Err(updateErr).Msgf("%s%s[CHOUETTE]%s Impossible de mettre à jour variables_total", boldColor, cyanColor, resetColor)
	}

	// -----------------------------------------------------------------------
	// Initialisation du solveur gaussien
	// -----------------------------------------------------------------------
	gSolver := solver.NewGaussianSolver(numUnknowns, trueValues)

	log.Info().Msgf(
		"%s%s[CHOUETTE]%s Moteur démarré | %d inconnues chargées | En attente d'équations...",
		boldColor, cyanColor, resetColor, numUnknowns,
	)

	// -----------------------------------------------------------------------
	// Contexte racine avec annulation sur signal système
	// -----------------------------------------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// -----------------------------------------------------------------------
	// Lancement du consumer Kafka pour le topic "equations"
	// -----------------------------------------------------------------------
	eqConsumer := consumer.NewEquationsConsumer(
		kafkaBrokers,
		"equations",
		"chouette-eq-group",
		gSolver,
		repo,
	)

	// -----------------------------------------------------------------------
	// Lancement du consumer Kafka pour le topic "noise"
	// -----------------------------------------------------------------------
	noiseConsumer := consumer.NewNoiseConsumer(
		kafkaBrokers,
		"noise",
		"chouette-noise-group",
		repo,
	)

	// Goroutine consumer equations
	go func() {
		if runErr := eqConsumer.Run(ctx); runErr != nil {
			log.Error().Err(runErr).Msgf("%s%s[CHOUETTE]%s Consumer equations terminé avec erreur", boldColor, cyanColor, resetColor)
		}
	}()

	// Goroutine consumer noise
	go func() {
		if runErr := noiseConsumer.Run(ctx); runErr != nil {
			log.Error().Err(runErr).Msgf("%s%s[CHOUETTE]%s Consumer noise terminé avec erreur", boldColor, cyanColor, resetColor)
		}
	}()

	// -----------------------------------------------------------------------
	// Attente du signal d'arrêt (SIGTERM en Docker, SIGINT en local Ctrl+C)
	// -----------------------------------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigCh
	log.Info().Msgf("%s%s[CHOUETTE]%s Signal reçu (%s) — arrêt propre en cours...", boldColor, cyanColor, resetColor, sig)

	// Annulation du contexte : tous les consumers s'arrêtent
	cancel()

	// Délai de grâce pour que les goroutines terminent proprement
	time.Sleep(2 * time.Second)

	log.Info().Msgf("%s%s[CHOUETTE]%s Moteur arrêté. Au revoir.", boldColor, cyanColor, resetColor)
}
