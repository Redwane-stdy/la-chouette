-- =============================================================================
-- La Chouette — Initialisation PostgreSQL
-- Exécuté automatiquement par le container postgres au premier démarrage
-- =============================================================================

-- Création de la base de données (déjà créée via POSTGRES_DB dans docker-compose)
-- Ce script s'exécute dans la base 'lachouette'

-- =============================================================================
-- TABLE: problem_variables
-- Stocke les inconnues générées et leurs valeurs vraies (connues du générateur)
-- =============================================================================
CREATE TABLE IF NOT EXISTS problem_variables (
    id          SERIAL PRIMARY KEY,
    name        VARCHAR(10) NOT NULL UNIQUE,   -- 'x1', 'x2', ..., 'x50'
    true_value  DOUBLE PRECISION NOT NULL,     -- valeur vraie générée
    created_at  TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- =============================================================================
-- TABLE: problem_equations
-- Stocke toutes les équations générées et leur assignation à une source
-- =============================================================================
CREATE TABLE IF NOT EXISTS problem_equations (
    id              SERIAL PRIMARY KEY,
    equation_id     VARCHAR(20) NOT NULL UNIQUE,  -- 'eq_001', 'eq_002', ...
    coefficients    JSONB NOT NULL,                -- {"x1": 2.0, "x3": -1.5, ...}
    constant        DOUBLE PRECISION NOT NULL,     -- valeur du membre droit
    assigned_source VARCHAR(20) NOT NULL,          -- 'alpha', 'beta', 'gamma'
    dispatch_order  INT NOT NULL,                  -- ordre de publication par la source
    dispatched      BOOLEAN NOT NULL DEFAULT FALSE, -- marqué true une fois publié
    created_at      TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Index pour optimiser la lecture par source et ordre
CREATE INDEX IF NOT EXISTS idx_equations_source_order
    ON problem_equations(assigned_source, dispatch_order);

-- =============================================================================
-- TABLE: equations_received
-- Toutes les équations reçues par le moteur La Chouette via Kafka
-- =============================================================================
CREATE TABLE IF NOT EXISTS equations_received (
    id          SERIAL PRIMARY KEY,
    equation_id VARCHAR(20) NOT NULL,
    source      VARCHAR(20) NOT NULL,
    coefficients JSONB NOT NULL,
    constant    DOUBLE PRECISION NOT NULL,
    received_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Index pour tri chronologique
CREATE INDEX IF NOT EXISTS idx_equations_received_at
    ON equations_received(received_at DESC);

-- =============================================================================
-- TABLE: resolved_variables
-- Variables dont la valeur a été calculée par le moteur
-- =============================================================================
CREATE TABLE IF NOT EXISTS resolved_variables (
    id                  SERIAL PRIMARY KEY,
    variable_name       VARCHAR(10) NOT NULL UNIQUE,
    computed_value      DOUBLE PRECISION NOT NULL,   -- valeur calculée par le solveur
    true_value          DOUBLE PRECISION,             -- valeur vraie (pour vérification)
    equations_count     INT NOT NULL,                 -- nb équations au moment de la résolution
    absolute_error      DOUBLE PRECISION,             -- |computed - true|
    resolved_at         TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- =============================================================================
-- TABLE: investigation_state
-- État global unique de l'investigation (une seule ligne, id=1)
-- =============================================================================
CREATE TABLE IF NOT EXISTS investigation_state (
    id                  INT PRIMARY KEY DEFAULT 1,
    status              VARCHAR(20) NOT NULL DEFAULT 'COLLECTING',
    -- COLLECTING: < 20 éq. | CORRELATING: 20-49 éq. | CONVERGING: 50+ éq. | SOLVED
    equations_received  INT NOT NULL DEFAULT 0,
    variables_resolved  INT NOT NULL DEFAULT 0,
    variables_total     INT NOT NULL DEFAULT 50,
    completion_pct      DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    experiment_started  BOOLEAN NOT NULL DEFAULT FALSE,
    started_at          TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    solved_at           TIMESTAMP WITH TIME ZONE,
    updated_at          TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Insertion de l'état initial
INSERT INTO investigation_state (id, status, experiment_started) VALUES (1, 'COLLECTING', FALSE)
ON CONFLICT (id) DO NOTHING;

-- =============================================================================
-- TABLE: noise_log
-- Log des messages de bruit reçus (pour affichage dans le dashboard)
-- =============================================================================
CREATE TABLE IF NOT EXISTS noise_log (
    id          SERIAL PRIMARY KEY,
    source      VARCHAR(30) NOT NULL,    -- 'noise_reuters', 'noise_bfm', etc.
    headline    TEXT NOT NULL,
    received_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Index pour les 20 derniers messages de bruit
CREATE INDEX IF NOT EXISTS idx_noise_received_at
    ON noise_log(received_at DESC);

-- =============================================================================
-- Trigger: mise à jour automatique de updated_at sur investigation_state
-- =============================================================================
CREATE OR REPLACE FUNCTION update_investigation_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_investigation_updated_at
    BEFORE UPDATE ON investigation_state
    FOR EACH ROW EXECUTE FUNCTION update_investigation_updated_at();

-- =============================================================================
-- Vue: résumé de l'investigation (utilisée par l'API)
-- =============================================================================
CREATE OR REPLACE VIEW v_investigation_summary AS
SELECT
    ist.status,
    ist.equations_received,
    ist.variables_resolved,
    ist.variables_total,
    ist.completion_pct,
    ist.started_at,
    ist.solved_at,
    ist.updated_at,
    EXTRACT(EPOCH FROM (COALESCE(ist.solved_at, NOW()) - ist.started_at))::INT AS elapsed_seconds,
    (SELECT COUNT(*) FROM noise_log) AS noise_messages_received,
    CASE ist.status
        WHEN 'COLLECTING'  THEN 'En cours d''investigation — Récolte d''informations...'
        WHEN 'CORRELATING' THEN 'Corrélation en cours — Premières variables identifiées'
        WHEN 'CONVERGING'  THEN 'Convergence détectée — La Chouette approche de la solution'
        WHEN 'SOLVED'      THEN '*** SYSTÈME RÉSOLU *** La Chouette a trouvé toutes les variables'
        ELSE 'Statut inconnu'
    END AS status_message
FROM investigation_state ist
WHERE ist.id = 1;
