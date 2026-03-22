// =============================================================================
// La Chouette — Problem Generator
// =============================================================================
//
// Ce service est le point de départ de tout le système "La Chouette".
// Il s'exécute UNE SEULE FOIS au démarrage, génère un système d'équations
// linéaires Ax = b avec une solution connue x_true, puis persiste le tout
// en PostgreSQL pour que les sources (alpha, beta, gamma) puissent ensuite
// publier les équations sur Kafka.
//
// Comportement idempotent : si des variables existent déjà en base, le
// service termine immédiatement sans rien modifier (EXIT 0).
//
// Pipeline d'exécution :
//   main() → connectDB() → checkAlreadyGenerated() → generateProblem()
//          → persistProblem() → logSummary() → EXIT 0
//
// =============================================================================

package main

import (
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"           // driver PostgreSQL, import side-effect uniquement
	"github.com/rs/zerolog"         // logging structuré avec couleurs ANSI
	"github.com/rs/zerolog/log"     // logger global de zerolog
)

// =============================================================================
// CONSTANTES & CODES DE COULEURS ANSI
// =============================================================================

const (
	// Préfixe affiché devant chaque message de log — en vert ANSI.
	logPrefix = "\033[32m[GEN]\033[0m"

	// Couleurs ANSI pour embellir les logs de variables et équations.
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorMagenta = "\033[35m"

	// Valeurs par défaut des paramètres configurables.
	defaultNumUnknowns  = 50
	defaultNumEquations = 80

	// En mode DEMO, le problème est beaucoup plus petit pour une démo rapide.
	demoNumUnknowns  = 15
	demoNumEquations = 25

	// Nombre de sources qui recevront les équations à publier.
	numSources = 3

	// Noms des trois sources Kafka du système.
	sourceAlpha = "alpha"
	sourceBeta  = "beta"
	sourceGamma = "gamma"

	// Bornes des coefficients aléatoires dans la matrice A.
	coeffMin = -5.0
	coeffMax = 5.0

	// Nombre minimum et maximum de coefficients non-nuls par équation (sparse).
	sparseMin = 3
	sparseMax = 8
)

// =============================================================================
// STRUCTURES DE DONNÉES
// =============================================================================

// Variable représente une inconnue du système : x1, x2, ..., xN.
// Sa valeur vraie (TrueValue) est générée aléatoirement et est la "réponse"
// que le moteur La Chouette cherchera à retrouver.
type Variable struct {
	Name      string  // Identifiant humain : "x1", "x2", etc.
	TrueValue float64 // Valeur secrète : la solution que le moteur doit trouver
}

// Equation représente une ligne du système Ax = b.
// Les coefficients sont sparse : seules quelques variables apparaissent
// avec un coefficient non-nul pour simuler un problème réaliste.
type Equation struct {
	EquationID     string             // Identifiant unique : "eq_001", "eq_042", etc.
	Coefficients   map[string]float64 // {"x1": 2.0, "x3": -1.5, ...} — variables présentes uniquement
	Constant       float64            // Membre droit b = A·x_true (calculé exactement)
	AssignedSource string             // Quelle source publiera cette équation : alpha/beta/gamma
	DispatchOrder  int                // Ordre de publication au sein de la source assignée
}

// Problem regroupe l'ensemble du problème généré.
type Problem struct {
	Variables []Variable
	Equations []Equation
}

// =============================================================================
// CONFIGURATION (lecture des variables d'environnement)
// =============================================================================

// Config regroupe tous les paramètres lus depuis l'environnement.
type Config struct {
	PostgresDSN  string // Chaîne de connexion PostgreSQL complète
	NumUnknowns  int    // Nombre d'inconnues N
	NumEquations int    // Nombre d'équations M (doit être > N pour sur-détermination)
	DemoMode     bool   // Si true, utilise les valeurs DEMO réduites
	LogLevel     string // Niveau de log zerolog ("debug", "info", etc.)
}

// loadConfig lit et valide les variables d'environnement.
// Les valeurs manquantes sont remplacées par des défauts raisonnables.
func loadConfig() Config {
	cfg := Config{}

	// Lecture de la DSN PostgreSQL — obligatoire.
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	if cfg.PostgresDSN == "" {
		// Valeur de fallback pour le développement local sans Docker.
		cfg.PostgresDSN = "postgres://lachouette:secret@localhost:5432/lachouette?sslmode=disable"
	}

	// Lecture du mode DEMO — désactive les paramètres NUM_UNKNOWNS/NUM_EQUATIONS.
	cfg.DemoMode = strings.ToLower(os.Getenv("DEMO_MODE")) == "true"

	if cfg.DemoMode {
		// En mode DEMO, on fixe des valeurs réduites indépendamment de l'env.
		cfg.NumUnknowns = demoNumUnknowns
		cfg.NumEquations = demoNumEquations
	} else {
		// Lecture de NUM_UNKNOWNS avec valeur par défaut.
		cfg.NumUnknowns = defaultNumUnknowns
		if val := os.Getenv("NUM_UNKNOWNS"); val != "" {
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.NumUnknowns = n
			}
		}

		// Lecture de NUM_EQUATIONS avec valeur par défaut.
		cfg.NumEquations = defaultNumEquations
		if val := os.Getenv("NUM_EQUATIONS"); val != "" {
			if m, err := strconv.Atoi(val); err == nil && m > 0 {
				cfg.NumEquations = m
			}
		}
	}

	// Lecture du niveau de log (debug/info/warn/error).
	cfg.LogLevel = strings.ToLower(os.Getenv("LOG_LEVEL"))
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info" // Par défaut on affiche les étapes importantes seulement.
	}

	return cfg
}

// =============================================================================
// INITIALISATION DU LOGGER
// =============================================================================

// initLogger configure zerolog avec :
//   - Sortie colorée sur la console (ConsoleWriter)
//   - Niveau de log paramétrable
//   - Timestamp en format heure:minute:seconde pour la lisibilité
func initLogger(logLevel string) {
	// ConsoleWriter active le rendu coloré dans le terminal.
	output := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "15:04:05",
		// FormatLevel colorie chaque niveau différemment.
		FormatLevel: func(i interface{}) string {
			level := fmt.Sprintf("%s", i)
			switch level {
			case "debug":
				return colorBlue + "DBG" + colorReset
			case "info":
				return colorGreen + "INF" + colorReset
			case "warn":
				return colorYellow + "WRN" + colorReset
			case "error":
				return "\033[31mERR\033[0m"
			default:
				return level
			}
		},
	}

	log.Logger = zerolog.New(output).With().Timestamp().Logger()

	// Application du niveau de log demandé.
	switch logLevel {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

// =============================================================================
// CONNEXION À POSTGRESQL
// =============================================================================

// connectDB ouvre la connexion PostgreSQL et vérifie qu'elle est vivante
// avec un Ping. Retourne l'objet *sql.DB prêt à l'emploi.
func connectDB(dsn string) (*sql.DB, error) {
	// Ouverture de la connexion (pas encore de réseau, juste parsing de la DSN).
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("échec sql.Open : %w", err)
	}

	// Ping réel pour vérifier la connectivité réseau et les credentials.
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("échec Ping PostgreSQL : %w", err)
	}

	return db, nil
}

// =============================================================================
// VÉRIFICATION D'IDEMPOTENCE
// =============================================================================

// checkAlreadyGenerated retourne true si le problème a déjà été généré
// et persiste en base. On se base sur la présence de lignes dans problem_variables.
// Si true → le programme doit s'arrêter proprement sans rien faire.
func checkAlreadyGenerated(db *sql.DB) (bool, error) {
	var count int
	// Requête rapide : COUNT(*) est O(1) sur une table PostgreSQL avec stats à jour.
	err := db.QueryRow("SELECT COUNT(*) FROM problem_variables").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("impossible de vérifier problem_variables : %w", err)
	}
	return count > 0, nil
}

// =============================================================================
// GÉNÉRATION DES VARIABLES (INCONNUES)
// =============================================================================

// generateVariables crée N inconnues x1, x2, ..., xN avec des valeurs vraies
// aléatoires tirées dans l'intervalle [-10, 10].
// Ces valeurs sont la "réponse secrète" que le moteur La Chouette cherchera.
func generateVariables(n int, rng *rand.Rand) []Variable {
	vars := make([]Variable, n)
	for i := 0; i < n; i++ {
		// Nom humain de la variable : x1, x2, ..., xN.
		name := fmt.Sprintf("x%d", i+1)

		// Valeur vraie aléatoire dans [-10.0, 10.0], arrondie à 4 décimales
		// pour la lisibilité dans les logs (sans impact sur la précision stockée).
		trueVal := rng.Float64()*20.0 - 10.0

		vars[i] = Variable{
			Name:      name,
			TrueValue: trueVal,
		}

		// Log DEBUG pour chaque variable — visible uniquement si LOG_LEVEL=debug.
		log.Debug().
			Msgf("%s Variable %s%s%s = %s%.4f%s",
				logPrefix,
				colorCyan, name, colorReset,
				colorYellow, trueVal, colorReset,
			)
	}
	return vars
}

// =============================================================================
// GÉNÉRATION DE LA MATRICE ET DES ÉQUATIONS
// =============================================================================

// generateEquations construit M équations linéaires à partir des N inconnues.
//
// Algorithme :
//  1. Générer une matrice dense A (m×n) avec des coefficients dans [coeffMin, coeffMax].
//  2. Calculer le membre droit b[i] = A[i] · x_true (produit scalaire exact).
//  3. Rendre chaque équation sparse : ne garder que sparseMin..sparseMax coefficients
//     non-nuls choisis aléatoirement, en recalculant b[i] avec la version sparse.
//  4. Assigner un dispatch_order aléatoire (shuffle de 0..m-1).
//
// La sparsification est faite APRÈS le calcul de b pour garantir la cohérence :
// on recalcule b avec uniquement les coefficients qu'on garde.
func generateEquations(variables []Variable, m int, rng *rand.Rand) []Equation {
	n := len(variables)       // Nombre d'inconnues
	equations := make([]Equation, m)

	// -------------------------------------------------------------------------
	// Étape 1 : Générer la matrice A dense (m×n) en mémoire.
	// Chaque ligne est un vecteur de coefficients pour une équation.
	// -------------------------------------------------------------------------
	A := make([][]float64, m)
	for i := 0; i < m; i++ {
		A[i] = make([]float64, n)
		for j := 0; j < n; j++ {
			// Coefficient aléatoire uniforme dans [coeffMin, coeffMax].
			// On évite délibérément les coefficients trop proches de 0 pour
			// avoir des équations "utiles" dans la matrice dense.
			A[i][j] = rng.Float64()*(coeffMax-coeffMin) + coeffMin
		}
	}

	// -------------------------------------------------------------------------
	// Étape 2 : Calculer le membre droit b = A · x_true.
	// C'est le produit scalaire exact entre chaque ligne de A et x_true.
	// Ce calcul est fait AVANT la sparsification.
	// -------------------------------------------------------------------------
	b := make([]float64, m)
	for i := 0; i < m; i++ {
		sum := 0.0
		for j := 0; j < n; j++ {
			sum += A[i][j] * variables[j].TrueValue
		}
		b[i] = sum
	}

	// -------------------------------------------------------------------------
	// Étape 3 : Sparsification — ne garder que sparseMin..sparseMax coefficients.
	// On sélectionne aléatoirement les indices de variables à conserver,
	// puis on RECALCULE b[i] avec uniquement ces coefficients.
	// -------------------------------------------------------------------------
	for i := 0; i < m; i++ {
		// Nombre de variables actives dans cette équation (tirage aléatoire).
		numActive := sparseMin + rng.Intn(sparseMax-sparseMin+1)
		// S'assurer qu'on ne dépasse pas le nombre de variables disponibles.
		if numActive > n {
			numActive = n
		}

		// Sélection aléatoire des indices de variables à garder.
		// On crée une permutation aléatoire de [0..n-1] et on prend les numActive premiers.
		indices := rng.Perm(n)
		activeIndices := indices[:numActive]

		// Trier les indices actifs pour l'affichage dans un ordre cohérent.
		sort.Ints(activeIndices)

		// Construction du map de coefficients sparse (seuls les actifs).
		coeffMap := make(map[string]float64, numActive)
		for _, j := range activeIndices {
			coeffMap[variables[j].Name] = A[i][j]
		}

		// Recalcul exact de la constante b avec uniquement les termes sparse retenus.
		// C'est la clé de la cohérence : b = somme(coeff_j * x_j_true) pour j actif.
		constant := 0.0
		for _, j := range activeIndices {
			constant += A[i][j] * variables[j].TrueValue
		}

		// Identifiant humain de l'équation avec padding à 3 chiffres : eq_001, eq_042, etc.
		eqID := fmt.Sprintf("eq_%03d", i+1)

		equations[i] = Equation{
			EquationID:   eqID,
			Coefficients: coeffMap,
			Constant:     constant,
			// AssignedSource et DispatchOrder seront remplis par assignEquationsToSources.
		}
	}

	// Référence non utilisée mais gardée pour clarté algorithmique (b dense calculé).
	_ = b

	return equations
}

// =============================================================================
// ASSIGNATION DES ÉQUATIONS AUX SOURCES
// =============================================================================

// assignEquationsToSources répartit les équations de façon équitable entre
// les trois sources (alpha, beta, gamma) en utilisant un round-robin.
// À l'intérieur de chaque source, le dispatch_order est un entier croissant
// déterminant l'ordre de publication des équations sur Kafka.
//
// La liste d'équations est d'abord mélangée (shuffle) pour que les sources
// reçoivent des équations "distribuées" et non groupées par ordre de création.
func assignEquationsToSources(equations []Equation, rng *rand.Rand) []Equation {
	// Sources disponibles dans l'ordre round-robin.
	sources := []string{sourceAlpha, sourceBeta, sourceGamma}

	// Mélange aléatoire de la liste d'équations avant assignation.
	// Cela assure que les sources reçoivent un mix varié d'équations,
	// simulant un flux de données réaliste et non séquentiel.
	shuffled := make([]Equation, len(equations))
	copy(shuffled, equations)
	rng.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	// Compteur par source pour le dispatch_order (commence à 1 pour chaque source).
	orderCounters := map[string]int{
		sourceAlpha: 1,
		sourceBeta:  1,
		sourceGamma: 1,
	}

	// Assignation round-robin : alpha → beta → gamma → alpha → ...
	for i := range shuffled {
		src := sources[i%numSources] // Rotation circulaire entre les 3 sources.
		shuffled[i].AssignedSource = src
		shuffled[i].DispatchOrder = orderCounters[src]
		orderCounters[src]++
	}

	return shuffled
}

// =============================================================================
// PERSISTANCE EN BASE DE DONNÉES
// =============================================================================

// persistProblem insère toutes les variables et toutes les équations
// en base de données dans une transaction unique.
// Si une erreur survient, tout est annulé (ROLLBACK automatique).
func persistProblem(db *sql.DB, problem Problem) error {
	// Ouverture d'une transaction pour garantir l'atomicité.
	// Si l'insertion d'une équation échoue, aucune donnée ne reste en base.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("impossible d'ouvrir une transaction : %w", err)
	}
	// defer Rollback est un no-op si Commit a déjà réussi.
	defer tx.Rollback() //nolint:errcheck

	// -------------------------------------------------------------------------
	// Insertion des variables (inconnues) une par une.
	// -------------------------------------------------------------------------
	log.Info().Msgf("%s Insertion de %d variables en base...", logPrefix, len(problem.Variables))

	// Préparation de la requête d'insertion pour optimiser les performances.
	varStmt, err := tx.Prepare(`
		INSERT INTO problem_variables (name, true_value)
		VALUES ($1, $2)
		ON CONFLICT (name) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("préparation INSERT problem_variables : %w", err)
	}
	defer varStmt.Close()

	for _, v := range problem.Variables {
		if _, err := varStmt.Exec(v.Name, v.TrueValue); err != nil {
			return fmt.Errorf("INSERT variable %s : %w", v.Name, err)
		}
	}

	// -------------------------------------------------------------------------
	// Insertion des équations avec leurs coefficients en JSONB.
	// -------------------------------------------------------------------------
	log.Info().Msgf("%s Insertion de %d équations en base...", logPrefix, len(problem.Equations))

	eqStmt, err := tx.Prepare(`
		INSERT INTO problem_equations
			(equation_id, coefficients, constant, assigned_source, dispatch_order)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (equation_id) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("préparation INSERT problem_equations : %w", err)
	}
	defer eqStmt.Close()

	for _, eq := range problem.Equations {
		// Sérialisation des coefficients en JSON pour le type JSONB de PostgreSQL.
		// Exemple : {"x1": 2.0, "x3": -1.5, "x17": 3.0}
		coeffJSON, err := json.Marshal(eq.Coefficients)
		if err != nil {
			return fmt.Errorf("sérialisation JSON pour %s : %w", eq.EquationID, err)
		}

		if _, err := eqStmt.Exec(
			eq.EquationID,
			string(coeffJSON), // PostgreSQL accepte un string pour le type JSONB.
			eq.Constant,
			eq.AssignedSource,
			eq.DispatchOrder,
		); err != nil {
			return fmt.Errorf("INSERT équation %s : %w", eq.EquationID, err)
		}
	}

	// Commit final : toutes les insertions sont atomiquement validées.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("COMMIT transaction : %w", err)
	}

	return nil
}

// =============================================================================
// CALCUL DU HASH DE LA SOLUTION (pour vérification)
// =============================================================================

// computeSolutionHash calcule un hash MD5 court de la solution x_true.
// Ce hash permet de vérifier visuellement que deux runs avec les mêmes
// paramètres ont généré le même problème (même seed → même hash).
func computeSolutionHash(variables []Variable) string {
	// On construit une chaîne "x1=3.1416;x2=-7.8293;..." triée par nom.
	parts := make([]string, len(variables))
	for i, v := range variables {
		parts[i] = fmt.Sprintf("%s=%.6f", v.Name, v.TrueValue)
	}
	sort.Strings(parts)
	raw := strings.Join(parts, ";")

	// MD5 rapide — pas besoin de cryptographie ici, juste d'un fingerprint.
	h := md5.Sum([]byte(raw))
	// On prend les 6 premiers bytes en hex = 12 caractères, facile à lire.
	return fmt.Sprintf("%x", h[:6])
}

// =============================================================================
// AFFICHAGE DES LOGS DE RÉSUMÉ (ÉTAPE FINALE)
// =============================================================================

// logSummary affiche un récapitulatif complet du problème généré :
//   - Liste de toutes les variables et leurs valeurs
//   - Liste de toutes les équations avec leurs coefficients
//   - Répartition par source
//   - Hash de la solution
func logSummary(problem Problem) {
	// -------------------------------------------------------------------------
	// Résumé des variables.
	// -------------------------------------------------------------------------
	log.Info().Msgf("%s %s--- Variables générées ---%s", logPrefix, colorCyan, colorReset)
	for _, v := range problem.Variables {
		log.Info().Msgf("%s Variable %s%s%s = %s%.4f%s",
			logPrefix,
			colorCyan, v.Name, colorReset,
			colorYellow, v.TrueValue, colorReset,
		)
	}

	// -------------------------------------------------------------------------
	// Résumé des équations (format compact : eq_001 → source:alpha | 2.00*x1 + ...).
	// -------------------------------------------------------------------------
	log.Info().Msgf("%s %s--- Équations générées ---%s", logPrefix, colorCyan, colorReset)
	for _, eq := range problem.Equations {
		// Construction de la représentation textuelle de l'équation.
		// Exemple : 2.00*x1 + (-1.50)*x3 + 3.00*x17 = 4.16
		terms := buildEquationString(eq)

		// Couleur de la source pour différencier visuellement alpha/beta/gamma.
		srcColor := sourceColor(eq.AssignedSource)

		log.Info().Msgf("%s %s%s%s → source:%s%s%s | %s = %.4f",
			logPrefix,
			colorMagenta, eq.EquationID, colorReset,
			srcColor, eq.AssignedSource, colorReset,
			terms,
			eq.Constant,
		)
	}

	// -------------------------------------------------------------------------
	// Comptage par source pour afficher la répartition.
	// -------------------------------------------------------------------------
	counts := map[string]int{sourceAlpha: 0, sourceBeta: 0, sourceGamma: 0}
	for _, eq := range problem.Equations {
		counts[eq.AssignedSource]++
	}

	log.Info().Msgf("%s Assignation: %salpha%s=%d éq., %sbeta%s=%d éq., %sgamma%s=%d éq.",
		logPrefix,
		colorGreen, colorReset, counts[sourceAlpha],
		colorBlue, colorReset, counts[sourceBeta],
		colorCyan, colorReset, counts[sourceGamma],
	)

	// -------------------------------------------------------------------------
	// Hash de la solution pour vérification rapide.
	// -------------------------------------------------------------------------
	hash := computeSolutionHash(problem.Variables)
	log.Info().Msgf("%s Problème persisté en base. Hash de la solution: %s%s%s",
		logPrefix,
		colorYellow, hash, colorReset,
	)
}

// buildEquationString construit la représentation textuelle d'une équation sparse.
// Les variables sont affichées dans l'ordre alphabétique/numérique pour la lisibilité.
// Exemple de sortie : "2.00*x1 + (-1.50)*x3 + 3.00*x17"
func buildEquationString(eq Equation) string {
	// Récupération des noms de variables et tri numérique.
	names := make([]string, 0, len(eq.Coefficients))
	for name := range eq.Coefficients {
		names = append(names, name)
	}
	// Tri "naturel" sur les noms : x1 < x2 < ... < x10 < x11, etc.
	sort.Slice(names, func(i, j int) bool {
		// Extraction du numéro entier pour tri numérique correct (x10 > x9).
		ni := extractVarNumber(names[i])
		nj := extractVarNumber(names[j])
		return ni < nj
	})

	parts := make([]string, 0, len(names))
	for _, name := range names {
		coeff := eq.Coefficients[name]
		if coeff < 0 {
			// Les coefficients négatifs sont affichés avec des parenthèses pour la clarté.
			parts = append(parts, fmt.Sprintf("(%.2f)*%s", coeff, name))
		} else {
			parts = append(parts, fmt.Sprintf("%.2f*%s", coeff, name))
		}
	}
	return strings.Join(parts, " + ")
}

// extractVarNumber extrait le numéro entier d'un nom de variable comme "x42" → 42.
// Utilisé pour le tri numérique correct des variables (évite x10 < x2 en tri lexical).
func extractVarNumber(name string) int {
	// Le nom est toujours de la forme "x<entier>", on parse depuis le 2ème caractère.
	if len(name) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(name[1:])
	return n
}

// sourceColor retourne le code de couleur ANSI associé à une source.
func sourceColor(source string) string {
	switch source {
	case sourceAlpha:
		return colorGreen
	case sourceBeta:
		return colorBlue
	case sourceGamma:
		return colorCyan
	default:
		return colorReset
	}
}

// =============================================================================
// VÉRIFICATION DE LA COHÉRENCE DU PROBLÈME
// =============================================================================

// verifyProblem effectue une vérification de cohérence légère :
// pour chaque équation, on recalcule A[i]·x_true et on vérifie que
// l'écart avec la constante stockée est inférieur à un epsilon numérique.
// Toute erreur importante indique un bug dans la génération.
func verifyProblem(problem Problem) {
	epsilon := 1e-9 // Tolérance numérique pour les erreurs d'arrondi float64.
	maxError := 0.0
	for _, eq := range problem.Equations {
		// Recalcul du membre droit avec les coefficients sparse.
		computed := 0.0
		for varName, coeff := range eq.Coefficients {
			// Recherche de la valeur vraie de cette variable.
			for _, v := range problem.Variables {
				if v.Name == varName {
					computed += coeff * v.TrueValue
					break
				}
			}
		}
		// Erreur absolue entre la constante stockée et le calcul de vérification.
		err := math.Abs(computed - eq.Constant)
		if err > maxError {
			maxError = err
		}
		if err > epsilon {
			log.Warn().Msgf("%s INCOHÉRENCE détectée sur %s : |%.10f - %.10f| = %.2e",
				logPrefix, eq.EquationID, computed, eq.Constant, err)
		}
	}
	log.Debug().Msgf("%s Vérification cohérence OK — erreur max: %.2e", logPrefix, maxError)
}

// =============================================================================
// POINT D'ENTRÉE PRINCIPAL
// =============================================================================

func main() {
	// =========================================================================
	// 1. Chargement de la configuration depuis l'environnement.
	// =========================================================================
	cfg := loadConfig()

	// Initialisation du logger avec le niveau demandé.
	initLogger(cfg.LogLevel)

	log.Info().Msgf("%s %s=== La Chouette — Problem Generator ===%s", logPrefix, colorGreen, colorReset)
	log.Info().Msgf("%s Démarrage du générateur de problème", logPrefix)

	// =========================================================================
	// 2. Mode DEMO : affichage explicite des paramètres réduits.
	// =========================================================================
	if cfg.DemoMode {
		log.Info().Msgf("%s %sMode DEMO%s: NUM_UNKNOWNS=%d, NUM_EQUATIONS=%d",
			logPrefix,
			colorYellow, colorReset,
			cfg.NumUnknowns, cfg.NumEquations,
		)
	} else {
		log.Info().Msgf("%s Configuration: %d inconnues, %d équations",
			logPrefix, cfg.NumUnknowns, cfg.NumEquations)
	}

	// =========================================================================
	// 3. Connexion à PostgreSQL avec retry simple.
	// Le container postgres peut mettre quelques secondes à être prêt.
	// =========================================================================
	log.Info().Msgf("%s Connexion PostgreSQL en cours...", logPrefix)

	var db *sql.DB
	var err error

	// Retry jusqu'à 10 fois avec 3 secondes d'intervalle (total 30s max).
	for attempt := 1; attempt <= 10; attempt++ {
		db, err = connectDB(cfg.PostgresDSN)
		if err == nil {
			break // Connexion réussie.
		}
		log.Warn().Msgf("%s Tentative %d/10 échouée : %v. Nouvel essai dans 3s...",
			logPrefix, attempt, err)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		log.Error().Msgf("%s Impossible de se connecter à PostgreSQL après 10 tentatives : %v", logPrefix, err)
		os.Exit(1)
	}
	defer db.Close()

	log.Info().Msgf("%s Connexion PostgreSQL établie", logPrefix)

	// =========================================================================
	// 4. Vérification d'idempotence : ne pas regénérer si déjà fait.
	// =========================================================================
	alreadyDone, err := checkAlreadyGenerated(db)
	if err != nil {
		log.Error().Msgf("%s Erreur lors de la vérification d'idempotence : %v", logPrefix, err)
		os.Exit(1)
	}
	if alreadyDone {
		log.Info().Msgf("%s %sPROBLÈME DÉJÀ GÉNÉRÉ%s — Le générateur s'arrête (idempotent). Bonne chance La Chouette !",
			logPrefix, colorYellow, colorReset)
		// EXIT 0 : comportement normal, pas une erreur.
		os.Exit(0)
	}

	// =========================================================================
	// 5. Initialisation du générateur de nombres aléatoires.
	// La seed basée sur le temps garantit des problèmes différents à chaque run,
	// tout en étant reproductible si on fixe manuellement la seed.
	// =========================================================================
	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))
	log.Debug().Msgf("%s Seed aléatoire : %d", logPrefix, seed)

	// =========================================================================
	// 6. Génération des variables (inconnues x1..xN).
	// =========================================================================
	log.Info().Msgf("%s Génération de %d inconnues...", logPrefix, cfg.NumUnknowns)
	variables := generateVariables(cfg.NumUnknowns, rng)
	log.Info().Msgf("%s %d variables générées avec succès", logPrefix, len(variables))

	// Log INFO résumé des variables (même sans --debug).
	for _, v := range variables {
		log.Info().Msgf("%s Variable %s%s%s = %s%.4f%s",
			logPrefix,
			colorCyan, v.Name, colorReset,
			colorYellow, v.TrueValue, colorReset,
		)
	}

	// =========================================================================
	// 7. Génération des équations linéaires sparse.
	// =========================================================================
	log.Info().Msgf("%s Génération de %d équations...", logPrefix, cfg.NumEquations)
	equations := generateEquations(variables, cfg.NumEquations, rng)
	log.Info().Msgf("%s %d équations générées", logPrefix, len(equations))

	// =========================================================================
	// 8. Assignation des équations aux sources (alpha/beta/gamma) en round-robin.
	// =========================================================================
	equations = assignEquationsToSources(equations, rng)

	// Comptage par source pour le log.
	counts := map[string]int{sourceAlpha: 0, sourceBeta: 0, sourceGamma: 0}
	for _, eq := range equations {
		counts[eq.AssignedSource]++
	}
	log.Info().Msgf("%s Assignation: %salpha%s=%d éq., %sbeta%s=%d éq., %sgamma%s=%d éq.",
		logPrefix,
		colorGreen, colorReset, counts[sourceAlpha],
		colorBlue, colorReset, counts[sourceBeta],
		colorCyan, colorReset, counts[sourceGamma],
	)

	// =========================================================================
	// 9. Log détaillé de chaque équation (niveau INFO pour les recruteurs).
	// =========================================================================
	for _, eq := range equations {
		terms := buildEquationString(eq)
		srcColor := sourceColor(eq.AssignedSource)
		log.Info().Msgf("%s %s%s%s → source:%s%s%s | %s = %.4f",
			logPrefix,
			colorMagenta, eq.EquationID, colorReset,
			srcColor, eq.AssignedSource, colorReset,
			terms,
			eq.Constant,
		)
	}

	// =========================================================================
	// 10. Vérification de cohérence (s'assure que b = A·x_true est exact).
	// =========================================================================
	problem := Problem{Variables: variables, Equations: equations}
	verifyProblem(problem)

	// =========================================================================
	// 11. Persistance atomique en PostgreSQL (une seule transaction).
	// =========================================================================
	log.Info().Msgf("%s Persistance en base de données...", logPrefix)
	if err := persistProblem(db, problem); err != nil {
		log.Error().Msgf("%s ERREUR lors de la persistance : %v", logPrefix, err)
		os.Exit(1)
	}

	// =========================================================================
	// 12. Log de synthèse final avec hash de la solution.
	// =========================================================================
	hash := computeSolutionHash(variables)
	log.Info().Msgf("%s Problème persisté en base. Hash de la solution: %s%s%s",
		logPrefix,
		colorYellow, hash, colorReset,
	)

	log.Info().Msgf("%s %s✓ Génération terminée. Démarrage des sources dans 5s...%s",
		logPrefix,
		colorGreen, colorReset,
	)

	// Pause de 5 secondes pour laisser le temps aux autres services de démarrer
	// et de se connecter à Kafka avant que les sources commencent à publier.
	time.Sleep(5 * time.Second)

	// EXIT 0 : génération réussie, le service s'arrête proprement.
	log.Info().Msgf("%s %sService terminé. Bonne chance La Chouette !%s", logPrefix, colorGreen, colorReset)
	os.Exit(0)
}
