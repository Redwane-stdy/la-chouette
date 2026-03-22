// Package solver implémente le moteur de résolution d'équations linéaires
// par élimination gaussienne incrémentale.
//
// Le solveur reçoit les équations une par une (via Kafka), construit
// progressivement la matrice augmentée [A|b] et tente à chaque ajout de
// résoudre de nouvelles variables par substitution et élimination partielle.
//
// Toutes les opérations sont protégées par un sync.RWMutex pour garantir
// la sûreté en environnement concurrent (plusieurs goroutines Kafka).
package solver

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// epsilon est le seuil en dessous duquel un coefficient est considéré nul.
// Toute valeur dont la valeur absolue est inférieure à epsilon est traitée
// comme zéro lors de l'élimination gaussienne.
const epsilon = 1e-9

// Status représente l'état courant du processus de résolution.
type Status string

const (
	// StatusCollecting indique que le solveur a reçu moins de 20 équations.
	// Pas assez d'information pour commencer la corrélation.
	StatusCollecting Status = "COLLECTING"

	// StatusCorrelating indique que le solveur a reçu au moins 20 équations
	// mais n'a pas encore résolu 30 % des variables.
	StatusCorrelating Status = "CORRELATING"

	// StatusConverging indique que plus de 30 % des variables sont résolues.
	// Le système converge vers la solution complète.
	StatusConverging Status = "CONVERGING"

	// StatusSolved indique que toutes les variables ont été résolues.
	StatusSolved Status = "SOLVED"
)

// SolverStatus agrège l'état courant du solveur : variables résolues,
// équations reçues, statut et message lisible pour les logs et Kafka.
type SolverStatus struct {
	// EquationsReceived est le nombre total d'équations reçues jusqu'ici.
	EquationsReceived int

	// VariablesResolved est le nombre de variables dont la valeur est connue.
	VariablesResolved int

	// VariablesTotal est le nombre d'inconnues attendu dans le système.
	VariablesTotal int

	// CompletionPct est le pourcentage de variables résolues (0.0 à 100.0).
	CompletionPct float64

	// Status est l'état courant du solveur (COLLECTING, CORRELATING, etc.).
	Status Status

	// StatusMessage est un message lisible décrivant l'état courant.
	StatusMessage string
}

// ResolvedVar représente une variable qui vient d'être résolue par le solveur.
// Elle porte la valeur calculée et, si disponible, l'erreur par rapport à la
// valeur vraie connue.
type ResolvedVar struct {
	// Name est le nom de la variable (ex: "x17").
	Name string

	// Value est la valeur calculée par élimination gaussienne.
	Value float64

	// TrueValue est la valeur vraie connue (issue de problem_variables).
	// Vaut 0.0 si la valeur vraie n'est pas disponible.
	TrueValue float64

	// AbsoluteError est |Value - TrueValue|. Vaut 0.0 si TrueValue est inconnu.
	AbsoluteError float64

	// HasTrueValue indique si TrueValue est disponible pour cette variable.
	HasTrueValue bool
}

// GaussianSolver est le cœur algorithmique du moteur La Chouette.
//
// Il maintient une matrice augmentée [A|b] qui grandit au fil des équations
// reçues, et tente après chaque ajout de résoudre de nouvelles variables
// par détection de lignes à un seul coefficient non-nul et propagation
// des valeurs connues (substitution back-substitution partielle).
type GaussianSolver struct {
	// numVars est le nombre total d'inconnues dans le système (ex: 50).
	numVars int

	// varIndex mappe le nom d'une variable à son indice de colonne dans la matrice.
	// Ex: "x1" → 0, "x2" → 1, ..., "x50" → 49.
	varIndex map[string]int

	// varNames est l'inverse de varIndex : indice → nom de variable.
	varNames []string

	// matrix est la matrice augmentée [A|b].
	// Chaque ligne est un vecteur de taille numVars+1 où les n premiers éléments
	// sont les coefficients et le dernier est la constante (membre droit).
	matrix [][]float64

	// resolved contient les variables dont la valeur a été calculée.
	// Clé : nom de variable ("x17"), valeur : valeur calculée (4.2).
	resolved map[string]float64

	// trueValues contient les valeurs vraies des variables, chargées depuis
	// problem_variables au démarrage. Utilisé uniquement pour calculer
	// l'erreur absolue et la loguer — pas pour la résolution.
	trueValues map[string]float64

	// equationsReceived compte le nombre total d'équations ajoutées au solveur.
	equationsReceived int

	// startTime est l'heure de création du solveur, utilisée pour calculer
	// le temps total jusqu'à la résolution complète.
	startTime time.Time

	// mu protège toutes les structures de données du solveur contre les
	// accès concurrents depuis les goroutines consumers Kafka.
	mu sync.RWMutex
}

// NewGaussianSolver crée et initialise un nouveau GaussianSolver.
//
// numVars est le nombre d'inconnues attendu (ex: 50 pour x1..x50).
// trueValues est la map des valeurs vraies chargées depuis PostgreSQL ;
// elle peut être vide si problem-generator n'a pas encore initialisé la BDD.
func NewGaussianSolver(numVars int, trueValues map[string]float64) *GaussianSolver {
	varIndex := make(map[string]int, numVars)
	varNames := make([]string, numVars)

	// Attribution des indices : x1→0, x2→1, ..., xN→N-1
	for i := 0; i < numVars; i++ {
		name := fmt.Sprintf("x%d", i+1)
		varIndex[name] = i
		varNames[i] = name
	}

	return &GaussianSolver{
		numVars:    numVars,
		varIndex:   varIndex,
		varNames:   varNames,
		matrix:     make([][]float64, 0, 128),
		resolved:   make(map[string]float64),
		trueValues: trueValues,
		startTime:  time.Now(),
	}
}

// AddEquation intègre une nouvelle équation dans le système et tente de
// résoudre de nouvelles variables.
//
// coefficients est la map nom→valeur des coefficients non-nuls de l'équation.
// constant est le membre droit de l'équation (b dans Ax = b).
//
// Retourne la liste des variables nouvellement résolues lors de cet appel.
// La liste peut être vide si aucune nouvelle variable n'a pu être déduite.
// Cette méthode est thread-safe.
func (s *GaussianSolver) AddEquation(coefficients map[string]float64, constant float64) []ResolvedVar {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.equationsReceived++

	// Construire le vecteur de la nouvelle équation (taille numVars+1)
	row := make([]float64, s.numVars+1)
	for varName, coeff := range coefficients {
		idx, ok := s.varIndex[varName]
		if !ok {
			// Variable inconnue dans notre index : on l'ignore
			continue
		}
		row[idx] = coeff
	}
	// Dernier élément = constante (membre droit)
	row[s.numVars] = constant

	// Ajouter la ligne à la matrice augmentée
	s.matrix = append(s.matrix, row)

	// Substituer les variables déjà connues dans toutes les lignes
	// (y compris la ligne qu'on vient d'ajouter)
	s.substituteKnown()

	// Tenter de résoudre de nouvelles variables par élimination partielle
	newlyResolved := s.tryResolve()

	return newlyResolved
}

// substituteKnown parcourt toutes les lignes de la matrice et remplace
// les coefficients des variables déjà résolues par leur valeur.
//
// Pour chaque variable résolue (valeur v, indice j) : pour chaque ligne i,
//   b_i ← b_i - A[i][j] * v
//   A[i][j] ← 0
//
// Cela réduit progressivement la matrice et facilite la détection de lignes
// à une seule variable non-résolue.
// Cette méthode doit être appelée sous verrou (s.mu est déjà acquis).
func (s *GaussianSolver) substituteKnown() {
	for varName, val := range s.resolved {
		j := s.varIndex[varName]
		for i := range s.matrix {
			coeff := s.matrix[i][j]
			if math.Abs(coeff) > epsilon {
				// Réduire la constante et annuler le coefficient
				s.matrix[i][s.numVars] -= coeff * val
				s.matrix[i][j] = 0.0
			}
		}
	}
}

// forwardEliminate effectue une élimination gaussienne progressive sur la matrice.
// Pour chaque colonne (variable non encore résolue), on cherche un pivot et on
// élimine cette variable de toutes les autres lignes. Cela crée une forme
// échelonnée qui permet ensuite la back-substitution.
//
// Cette méthode doit être appelée sous verrou (s.mu est déjà acquis).
func (s *GaussianSolver) forwardEliminate() {
	n := len(s.matrix)
	if n == 0 {
		return
	}

	pivotRow := 0 // prochaine ligne disponible comme pivot

	for col := 0; col < s.numVars && pivotRow < n; col++ {
		// Trouver la ligne avec le plus grand |coefficient| dans cette colonne
		// (pivot partiel pour stabilité numérique)
		bestRow := -1
		bestAbs := epsilon
		for i := pivotRow; i < n; i++ {
			if abs := math.Abs(s.matrix[i][col]); abs > bestAbs {
				bestAbs = abs
				bestRow = i
			}
		}

		if bestRow == -1 {
			// Colonne entièrement nulle — passer à la suivante
			continue
		}

		// Échanger la ligne pivot avec la ligne courante
		s.matrix[pivotRow], s.matrix[bestRow] = s.matrix[bestRow], s.matrix[pivotRow]

		// Normaliser la ligne pivot
		pivot := s.matrix[pivotRow][col]
		for j := col; j <= s.numVars; j++ {
			s.matrix[pivotRow][j] /= pivot
		}

		// Éliminer cette variable de toutes les AUTRES lignes
		for i := 0; i < n; i++ {
			if i == pivotRow {
				continue
			}
			factor := s.matrix[i][col]
			if math.Abs(factor) <= epsilon {
				continue
			}
			for j := col; j <= s.numVars; j++ {
				s.matrix[i][j] -= factor * s.matrix[pivotRow][j]
			}
		}

		pivotRow++
	}
}

// tryResolve examine chaque ligne de la matrice à la recherche de lignes
// à exactement un coefficient non-nul. Une telle ligne permet de déduire
// directement la valeur de la variable correspondante.
//
// Quand suffisamment d'équations sont disponibles, effectue d'abord une
// élimination gaussienne complète pour mettre la matrice en forme échelonnée.
//
// Retourne la liste des variables nouvellement résolues.
// Cette méthode doit être appelée sous verrou (s.mu est déjà acquis).
func (s *GaussianSolver) tryResolve() []ResolvedVar {
	var newlyResolved []ResolvedVar

	// Dès qu'on a reçu suffisamment d'équations (au moins autant que d'inconnues
	// restantes), on lance l'élimination gaussienne complète pour construire
	// une forme échelonnée exploitable par la back-substitution.
	unresolvedCount := s.numVars - len(s.resolved)
	if len(s.matrix) >= unresolvedCount && unresolvedCount > 0 {
		s.forwardEliminate()
	}

	// Boucle de propagation : on continue tant que de nouvelles variables
	// sont trouvées à chaque itération.
	for {
		somethingChanged := false

		for i := range s.matrix {
			// Compter les coefficients non-nuls sur cette ligne
			nonZeroCount := 0
			nonZeroIdx := -1

			for j := 0; j < s.numVars; j++ {
				if math.Abs(s.matrix[i][j]) > epsilon {
					nonZeroCount++
					nonZeroIdx = j
				}
			}

			// Une seule variable non-résolue sur cette ligne :
			// on peut déduire directement sa valeur.
			if nonZeroCount == 1 && nonZeroIdx >= 0 {
				varName := s.varNames[nonZeroIdx]

				// Ne pas résoudre deux fois la même variable
				if _, alreadyResolved := s.resolved[varName]; alreadyResolved {
					continue
				}

				coeff := s.matrix[i][nonZeroIdx]
				constant := s.matrix[i][s.numVars]

				// x = b / a  (après élimination, coeff ≈ 1.0)
				value := constant / coeff

				// Enregistrer la valeur résolue
				s.resolved[varName] = value

				// Calculer l'erreur absolue par rapport à la valeur vraie
				rv := ResolvedVar{
					Name:  varName,
					Value: value,
				}
				if tv, hasTrueVal := s.trueValues[varName]; hasTrueVal {
					rv.TrueValue = tv
					rv.AbsoluteError = math.Abs(value - tv)
					rv.HasTrueValue = true
				}

				newlyResolved = append(newlyResolved, rv)
				somethingChanged = true

				// Annuler le coefficient dans la ligne (la variable est résolue)
				s.matrix[i][nonZeroIdx] = 0.0
				s.matrix[i][s.numVars] = 0.0
			}
		}

		if !somethingChanged {
			break
		}

		// Propager les nouvelles valeurs dans toute la matrice
		s.substituteKnown()
	}

	return newlyResolved
}

// GetStatus retourne l'état courant du solveur de manière thread-safe.
// Le statut est calculé à la volée en fonction du nombre d'équations reçues
// et du nombre de variables résolues.
func (s *GaussianSolver) GetStatus() SolverStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.buildStatus()
}

// buildStatus calcule et retourne le SolverStatus courant.
// Cette méthode doit être appelée sous verrou (mu.RLock ou mu.Lock déjà acquis).
func (s *GaussianSolver) buildStatus() SolverStatus {
	resolved := len(s.resolved)
	received := s.equationsReceived
	total := s.numVars
	pct := 0.0
	if total > 0 {
		pct = float64(resolved) / float64(total) * 100.0
	}

	// Détermination du statut selon les règles métier
	var status Status
	var msg string

	switch {
	case resolved == total && total > 0:
		elapsed := time.Since(s.startTime)
		status = StatusSolved
		msg = fmt.Sprintf("*** SYSTÈME RÉSOLU *** | Toutes les %d variables trouvées | Temps: %s",
			total, formatDuration(elapsed))

	case float64(resolved) >= 0.3*float64(total):
		status = StatusConverging
		msg = fmt.Sprintf("Convergence détectée — %d variables identifiées sur %d", resolved, total)

	case received >= 20:
		status = StatusCorrelating
		msg = fmt.Sprintf("Corrélation en cours — %d variables identifiées sur %d", resolved, total)

	default:
		status = StatusCollecting
		msg = fmt.Sprintf("Récolte d'informations — %d équations reçues", received)
	}

	return SolverStatus{
		EquationsReceived: received,
		VariablesResolved: resolved,
		VariablesTotal:    total,
		CompletionPct:     pct,
		Status:            status,
		StatusMessage:     msg,
	}
}

// formatDuration formate une durée en format lisible "Xm Ys".
func formatDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	if minutes > 0 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// EquationsReceived retourne le nombre d'équations reçues de manière thread-safe.
func (s *GaussianSolver) EquationsReceived() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.equationsReceived
}

// VariablesResolved retourne le nombre de variables résolues de manière thread-safe.
func (s *GaussianSolver) VariablesResolved() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.resolved)
}
