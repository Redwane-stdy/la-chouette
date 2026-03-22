// Package store fournit la couche de persistance PostgreSQL du moteur La Chouette.
//
// Il expose un Repository qui encapsule toutes les opérations d'écriture et
// de lecture nécessaires au moteur : sauvegarde des équations reçues,
// persistance des variables résolues, mise à jour de l'état de l'investigation
// et log du bruit.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Repository est le dépôt de données PostgreSQL du moteur.
// Toutes les méthodes sont stateless et thread-safe car elles s'appuient
// sur le pool de connexions de database/sql.
type Repository struct {
	db *sql.DB
}

// NewRepository crée un nouveau Repository à partir d'une connexion *sql.DB
// déjà ouverte et vérifiée (Ping réussi).
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// LoadTrueValues charge depuis la table problem_variables toutes les valeurs
// vraies des inconnues du problème.
//
// Ces valeurs sont générées par problem-generator et servent uniquement à
// calculer l'erreur absolue lors de la résolution (elles ne participent pas
// à l'algorithme gaussien).
//
// Retourne une map nom→valeur (ex: {"x1": 3.14, "x2": -2.71, ...}).
func (r *Repository) LoadTrueValues() (map[string]float64, error) {
	rows, err := r.db.Query(`SELECT name, true_value FROM problem_variables`)
	if err != nil {
		return nil, fmt.Errorf("LoadTrueValues: query: %w", err)
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var name string
		var val float64
		if err := rows.Scan(&name, &val); err != nil {
			return nil, fmt.Errorf("LoadTrueValues: scan: %w", err)
		}
		result[name] = val
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("LoadTrueValues: rows error: %w", err)
	}
	return result, nil
}

// SaveEquationReceived persiste une équation reçue via Kafka dans la table
// equations_received.
//
// equationID est l'identifiant unique de l'équation (ex: "eq_023").
// source identifie la source émettrice ("alpha", "beta", "gamma").
// coefficients est la map des coefficients de l'équation.
// constant est le membre droit de l'équation.
func (r *Repository) SaveEquationReceived(equationID, source string, coefficients map[string]float64, constant float64) error {
	coeffJSON, err := json.Marshal(coefficients)
	if err != nil {
		return fmt.Errorf("SaveEquationReceived: marshal: %w", err)
	}

	_, err = r.db.Exec(
		`INSERT INTO equations_received (equation_id, source, coefficients, constant)
		 VALUES ($1, $2, $3, $4)`,
		equationID, source, string(coeffJSON), constant,
	)
	if err != nil {
		return fmt.Errorf("SaveEquationReceived: insert: %w", err)
	}
	return nil
}

// SaveResolvedVariable persiste une variable résolue dans la table resolved_variables.
// En cas de conflit sur variable_name (résolution ultérieure), la valeur calculée
// est mise à jour (ON CONFLICT DO UPDATE).
//
// variableName est le nom de la variable (ex: "x17").
// computedValue est la valeur calculée par le solveur gaussien.
// trueValue est la valeur vraie connue (0.0 si non disponible).
// equationsCount est le nombre d'équations reçues au moment de la résolution.
// absoluteError est |computedValue - trueValue|.
func (r *Repository) SaveResolvedVariable(variableName string, computedValue, trueValue float64, equationsCount int, absoluteError float64) error {
	_, err := r.db.Exec(
		`INSERT INTO resolved_variables (variable_name, computed_value, true_value, equations_count, absolute_error)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (variable_name) DO UPDATE SET
		   computed_value = EXCLUDED.computed_value,
		   true_value     = EXCLUDED.true_value,
		   equations_count = EXCLUDED.equations_count,
		   absolute_error = EXCLUDED.absolute_error,
		   resolved_at    = NOW()`,
		variableName, computedValue, trueValue, equationsCount, absoluteError,
	)
	if err != nil {
		return fmt.Errorf("SaveResolvedVariable: upsert: %w", err)
	}
	return nil
}

// UpdateInvestigationState met à jour l'état global de l'investigation dans la
// table investigation_state (une seule ligne, id=1).
//
// status est le statut courant ("COLLECTING", "CORRELATING", "CONVERGING", "SOLVED").
// equationsReceived est le compteur global d'équations reçues.
// variablesResolved est le nombre de variables résolues.
// completionPct est le pourcentage de complétion (0.0 à 100.0).
func (r *Repository) UpdateInvestigationState(status string, equationsReceived, variablesResolved int, completionPct float64) error {
	// Note: pq ne supporte pas la réutilisation du même $N dans une requête.
	// On passe status deux fois ($1 et $5) pour contourner cette limitation.
	_, err := r.db.Exec(
		`UPDATE investigation_state SET
		   status             = $1,
		   equations_received = $2,
		   variables_resolved = $3,
		   completion_pct     = $4,
		   solved_at          = CASE WHEN $5 = 'SOLVED' THEN NOW() ELSE solved_at END
		 WHERE id = 1`,
		status, equationsReceived, variablesResolved, completionPct, status,
	)
	if err != nil {
		return fmt.Errorf("UpdateInvestigationState: update: %w", err)
	}
	return nil
}

// SaveNoiseMessage persiste un message de bruit dans la table noise_log.
//
// source identifie l'émetteur du bruit (ex: "noise_reuters", "noise_bfm").
// headline est le contenu textuel du message de bruit.
func (r *Repository) SaveNoiseMessage(source, headline string) error {
	_, err := r.db.Exec(
		`INSERT INTO noise_log (source, headline) VALUES ($1, $2)`,
		source, headline,
	)
	if err != nil {
		return fmt.Errorf("SaveNoiseMessage: insert: %w", err)
	}
	return nil
}

// GetInvestigationSummary retourne un résumé de l'état de l'investigation
// depuis la vue v_investigation_summary. Utile pour les logs de démarrage.
func (r *Repository) GetInvestigationSummary() (status string, equationsReceived, variablesResolved int, err error) {
	row := r.db.QueryRow(
		`SELECT status, equations_received, variables_resolved
		 FROM investigation_state WHERE id = 1`,
	)
	err = row.Scan(&status, &equationsReceived, &variablesResolved)
	if err == sql.ErrNoRows {
		return "COLLECTING", 0, 0, nil
	}
	return
}

// NoiseMessageCount retourne le nombre total de messages de bruit enregistrés.
// Utilisé pour les métriques du dashboard.
func (r *Repository) NoiseMessageCount() (int, error) {
	var count int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM noise_log`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("NoiseMessageCount: %w", err)
	}
	return count, nil
}

// Ping vérifie que la connexion à la base de données est toujours active.
func (r *Repository) Ping() error {
	return r.db.Ping()
}

