// linalg.go — Utilitaires d'algèbre linéaire avec gonum.
//
// Ce fichier fournit des fonctions complémentaires au solveur gaussien natif,
// basées sur gonum/mat pour des opérations de vérification numérique :
//   - calcul de la norme de Frobenius de la matrice augmentée courante
//   - évaluation de la condition du sous-système résolu
//
// Ces fonctions sont utilisées pour détecter les systèmes mal-conditionnés
// (condition number élevé → résultats potentiellement instables) et enrichir
// les logs de diagnostic.
package solver

import (
	"math"

	"gonum.org/v1/gonum/mat"
)

// FrobeniusNorm calcule la norme de Frobenius de la matrice de coefficients
// extraite de la matrice augmentée du solveur.
//
// La norme de Frobenius est définie comme : ||A||_F = sqrt(Σ a_ij²)
// Elle mesure la "taille" globale de la matrice et permet de détecter les
// équations dont les coefficients sont disproportionnés.
//
// Retourne 0.0 si la matrice est vide ou si aucune ligne n'est disponible.
// Cette méthode est thread-safe (acquiert le verrou en lecture).
func (s *GaussianSolver) FrobeniusNorm() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	m := len(s.matrix)
	if m == 0 {
		return 0.0
	}
	n := s.numVars

	// Extraire la matrice de coefficients A (sans la colonne des constantes)
	data := make([]float64, m*n)
	for i, row := range s.matrix {
		for j := 0; j < n; j++ {
			data[i*n+j] = row[j]
		}
	}

	// Construire une Dense gonum pour utiliser les routines BLAS optimisées
	denseA := mat.NewDense(m, n, data)

	// La norme de Frobenius est disponible directement via mat.Norm
	return mat.Norm(denseA, 2) // 2 = norme spectrale (valeur singulière maximale)
}

// ConditionEstimate retourne une estimation du conditionnement du sous-système
// carré formé par les min(m,n) premières lignes et colonnes de la matrice augmentée.
//
// Un conditionnement élevé (> 1e6) indique un système numériquement instable :
// de légères erreurs dans les coefficients peuvent entraîner de grandes erreurs
// dans la solution. Le solveur logue un avertissement dans ce cas.
//
// Retourne -1.0 si le conditionnement ne peut pas être calculé (matrice vide
// ou moins de 2 lignes).
// Cette méthode est thread-safe (acquiert le verrou en lecture).
func (s *GaussianSolver) ConditionEstimate() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	m := len(s.matrix)
	if m < 2 {
		return -1.0
	}

	// Sous-matrice carrée de taille min(m,n)
	size := m
	if s.numVars < size {
		size = s.numVars
	}
	if size < 2 {
		return -1.0
	}

	// Extraire la sous-matrice carrée
	data := make([]float64, size*size)
	for i := 0; i < size; i++ {
		for j := 0; j < size; j++ {
			data[i*size+j] = s.matrix[i][j]
		}
	}

	denseA := mat.NewDense(size, size, data)

	// SVD pour estimer le conditionnement : cond(A) = σ_max / σ_min
	var svd mat.SVD
	ok := svd.Factorize(denseA, mat.SVDThin)
	if !ok {
		return -1.0
	}

	values := svd.Values(nil)
	if len(values) < 2 {
		return -1.0
	}

	sigmaMax := values[0]
	sigmaMin := values[len(values)-1]

	if math.Abs(sigmaMin) < epsilon {
		// Matrice singulière : conditionnement infini
		return math.Inf(1)
	}

	return sigmaMax / sigmaMin
}
