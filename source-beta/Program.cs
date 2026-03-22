// =============================================================================
// source-beta — Producteur Kafka pour les équations assignées à la source BETA
//
// Rôle : lire les équations depuis PostgreSQL, les publier sur le topic
//        "equations" via Kafka, puis les marquer comme dispatched.
//
// Variables d'environnement :
//   KAFKA_BROKERS           adresse du broker Kafka (ex: kafka:9092)
//   POSTGRES_DSN            chaîne de connexion PostgreSQL
//   SOURCE_BETA_INTERVAL_MS intervalle entre publications en ms (défaut: 4000)
//   DEMO_MODE               si "true", divise l'intervalle par 3
//   LOG_LEVEL               niveau de log (défaut: Information)
// =============================================================================

using System.Text.Json;
using System.Text.Json.Serialization;
using Confluent.Kafka;
using Microsoft.Extensions.Logging;
using Npgsql;

// ---------------------------------------------------------------------------
// Couleurs ANSI — préfixe [BETA] en vert pour identifier ce service dans les
// logs agrégés de docker compose
// ---------------------------------------------------------------------------
const string PREFIXE_BETA  = "\x1b[32m[BETA]\x1b[0m";
const string PREFIXE_ERREUR = "\x1b[31m[BETA]\x1b[0m";
const string PREFIXE_WARN  = "\x1b[33m[BETA]\x1b[0m";

// ---------------------------------------------------------------------------
// Lecture des variables d'environnement
// ---------------------------------------------------------------------------
var kafkaBrokers   = Env("KAFKA_BROKERS",           "kafka:9092");
var postgresDsn    = Env("POSTGRES_NPGSQL_DSN",       "Host=postgres;Port=5432;Username=lachouette;Password=secret;Database=lachouette;");
var intervalMs     = int.Parse(Env("SOURCE_BETA_INTERVAL_MS", "4000"));
var demoMode       = Env("DEMO_MODE", "false").ToLowerInvariant() == "true";
var logLevelStr    = Env("LOG_LEVEL", "Information");

// En mode démo : accélère la publication d'un facteur 3
if (demoMode)
{
    intervalMs = intervalMs / 3;
    Log($"Mode DEMO activé — intervalle réduit à {intervalMs} ms");
}

// ---------------------------------------------------------------------------
// Configuration du logger Microsoft.Extensions.Logging
// ---------------------------------------------------------------------------
var logLevel = logLevelStr switch
{
    "Debug"       => LogLevel.Debug,
    "Warning"     => LogLevel.Warning,
    "Error"       => LogLevel.Error,
    "Critical"    => LogLevel.Critical,
    _             => LogLevel.Information
};

using var loggerFactory = LoggerFactory.Create(builder =>
{
    builder.SetMinimumLevel(logLevel);
    builder.AddConsole();
});
var logger = loggerFactory.CreateLogger("source-beta");

// ---------------------------------------------------------------------------
// Gestion du CancellationToken — écoute SIGTERM et SIGINT (Ctrl+C)
// ---------------------------------------------------------------------------
using var cts = new CancellationTokenSource();
Console.CancelKeyPress += (_, e) =>
{
    e.Cancel = true; // empêche la terminaison brutale du processus
    Log($"{PREFIXE_WARN} Signal d'arrêt reçu — arrêt propre en cours...");
    cts.Cancel();
};
AppDomain.CurrentDomain.ProcessExit += (_, _) => cts.Cancel();

var token = cts.Token;

// ---------------------------------------------------------------------------
// ÉTAPE 1 : Attendre que PostgreSQL soit disponible
// Retry loop : 10 tentatives, 5 secondes entre chaque
// ---------------------------------------------------------------------------
Log($"{PREFIXE_BETA} Connexion à PostgreSQL ({postgresDsn[..Math.Min(50, postgresDsn.Length)]}...)");

NpgsqlConnection? connexionPg = null;
const int MAX_TENTATIVES_PG = 10;

for (int tentative = 1; tentative <= MAX_TENTATIVES_PG; tentative++)
{
    try
    {
        connexionPg = new NpgsqlConnection(postgresDsn);
        await connexionPg.OpenAsync(token);

        // Vérification rapide que la BDD répond
        await using var cmd = connexionPg.CreateCommand();
        cmd.CommandText = "SELECT 1";
        await cmd.ExecuteScalarAsync(token);

        Log($"{PREFIXE_BETA} Connexion PostgreSQL établie");

        // Réinitialisation du flag dispatched au démarrage — permet le rejeu complet
        // si le container est redémarré sans purger le volume PostgreSQL.
        await using var cmdReset = connexionPg.CreateCommand();
        cmdReset.CommandText = "UPDATE problem_equations SET dispatched = FALSE WHERE assigned_source = 'beta'";
        int rowsReset = await cmdReset.ExecuteNonQueryAsync(token);
        Log($"{PREFIXE_BETA} Réinitialisation : {rowsReset} équation(s) marquées non-dispatched.");
        break;
    }
    catch (Exception ex) when (tentative < MAX_TENTATIVES_PG)
    {
        Log($"{PREFIXE_WARN} PostgreSQL non disponible (tentative {tentative}/{MAX_TENTATIVES_PG}) : {ex.Message}");
        connexionPg?.Dispose();
        connexionPg = null;
        await Task.Delay(5000, token);
    }
}

if (connexionPg is null)
{
    Log($"{PREFIXE_ERREUR} Impossible de se connecter à PostgreSQL après {MAX_TENTATIVES_PG} tentatives. Arrêt.");
    return 1;
}

// ── Attente du démarrage de l'expérience ──────────────────────────────────
// L'utilisateur doit cliquer START sur http://localhost:3000
Log($"{PREFIXE_WARN} En attente du démarrage — ouvrez http://localhost:3000 et cliquez START");
while (!token.IsCancellationRequested)
{
    try
    {
        await using var cmdReady = connexionPg.CreateCommand();
        cmdReady.CommandText = "SELECT experiment_started FROM investigation_state WHERE id = 1";
        var resultReady = await cmdReady.ExecuteScalarAsync(token);
        bool experimentStarted = resultReady is bool b && b;

        if (experimentStarted)
        {
            Log($"{PREFIXE_BETA} Expérience démarrée ! Publication des équations...");
            break;
        }

        Log($"{PREFIXE_BETA} En attente du signal START sur http://localhost:3000 ...");
    }
    catch (Exception ex)
    {
        Log($"{PREFIXE_ERREUR} Erreur lecture experiment_started : {ex.Message}");
    }

    try { await Task.Delay(2000, token); }
    catch (TaskCanceledException) { return 1; }
}

// ---------------------------------------------------------------------------
// ÉTAPE 2 : Attendre que les équations "beta" existent dans la BDD
// Le problem-generator doit avoir terminé son initialisation avant source-beta.
// Retry loop : 30 tentatives, 5 secondes entre chaque (max 2m30s d'attente)
// ---------------------------------------------------------------------------
Log($"{PREFIXE_BETA} En attente des équations assignées à 'beta' dans PostgreSQL...");

const int MAX_TENTATIVES_EQ = 30;
int nombreEquations = 0;

for (int tentative = 1; tentative <= MAX_TENTATIVES_EQ; tentative++)
{
    try
    {
        await using var cmd = connexionPg.CreateCommand();
        cmd.CommandText = @"
            SELECT COUNT(*)
            FROM problem_equations
            WHERE assigned_source = 'beta' AND dispatched = FALSE";

        var resultat = await cmd.ExecuteScalarAsync(token);
        nombreEquations = Convert.ToInt32(resultat);

        if (nombreEquations > 0)
        {
            Log($"{PREFIXE_BETA} {nombreEquations} équations assignées trouvées en base.");
            break;
        }

        Log($"{PREFIXE_WARN} Aucune équation 'beta' disponible (tentative {tentative}/{MAX_TENTATIVES_EQ}) — nouveau test dans 5s...");
        await Task.Delay(5000, token);
    }
    catch (Exception ex)
    {
        Log($"{PREFIXE_ERREUR} Erreur lors de la vérification des équations : {ex.Message}");
        await Task.Delay(5000, token);
    }
}

if (nombreEquations == 0)
{
    Log($"{PREFIXE_ERREUR} Aucune équation 'beta' après {MAX_TENTATIVES_EQ} tentatives. Arrêt.");
    connexionPg.Dispose();
    return 1;
}

// ---------------------------------------------------------------------------
// ÉTAPE 3 : Connexion Kafka
// ---------------------------------------------------------------------------
Log($"{PREFIXE_BETA} Connexion au broker Kafka ({kafkaBrokers})...");

var configProducer = new ProducerConfig
{
    BootstrapServers = kafkaBrokers,
    // Attendre l'accusé de réception de tous les réplicas (fiabilité maximale)
    Acks = Acks.All,
    // Retry automatique en cas d'erreur transitoire
    MessageSendMaxRetries = 5,
    RetryBackoffMs = 1000,
    // Timeout global de livraison d'un message
    MessageTimeoutMs = 30000,
};

IProducer<string, string>? producteur = null;
try
{
    producteur = new ProducerBuilder<string, string>(configProducer).Build();
    Log($"{PREFIXE_BETA} Connexion Kafka établie");
}
catch (Exception ex)
{
    Log($"{PREFIXE_ERREUR} Échec de la connexion Kafka : {ex.Message}");
    connexionPg.Dispose();
    return 1;
}

// ---------------------------------------------------------------------------
// ÉTAPE 4 : Charger toutes les équations beta non dispatched depuis PostgreSQL
// ---------------------------------------------------------------------------
Log($"{PREFIXE_BETA} Chargement des équations depuis PostgreSQL...");

var equations = new List<EquationBeta>();
try
{
    await using var cmdCharge = connexionPg.CreateCommand();
    cmdCharge.CommandText = @"
        SELECT equation_id, coefficients::text, constant, dispatch_order
        FROM problem_equations
        WHERE assigned_source = 'beta' AND dispatched = FALSE
        ORDER BY dispatch_order ASC";

    await using var reader = await cmdCharge.ExecuteReaderAsync(token);
    while (await reader.ReadAsync(token))
    {
        var equationId    = reader.GetString(0);
        var coefficientsJson = reader.GetString(1);
        var constant      = reader.GetDouble(2);
        var dispatchOrder = reader.GetInt32(3);

        // Désérialisation du JSONB PostgreSQL en dictionnaire C#
        var coefficients = JsonSerializer.Deserialize<Dictionary<string, double>>(coefficientsJson)
                           ?? new Dictionary<string, double>();

        equations.Add(new EquationBeta(equationId, coefficients, constant, dispatchOrder));
    }
}
catch (Exception ex)
{
    Log($"{PREFIXE_ERREUR} Erreur lors du chargement des équations : {ex.Message}");
    producteur.Dispose();
    connexionPg.Dispose();
    return 1;
}

Log($"{PREFIXE_BETA} {equations.Count} équations assignées. Début de la publication dans {intervalMs / 1000.0:F1}s...");

// Petite pause initiale pour laisser le pipeline se stabiliser
await Task.Delay(intervalMs, token);

// ---------------------------------------------------------------------------
// ÉTAPE 5 : Boucle principale de publication
// Pour chaque équation :
//   a) Sérialiser en JSON
//   b) Publier sur topic "equations"
//   c) Marquer dispatched dans PostgreSQL
//   d) Logger les détails
//   e) Attendre l'intervalle avec jitter ±10 %
// ---------------------------------------------------------------------------
const string TOPIC_EQUATIONS = "equations";

var optionsJson = new JsonSerializerOptions
{
    PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
    WriteIndented = false,
};

for (int i = 0; i < equations.Count; i++)
{
    // Vérification de l'annulation avant chaque publication
    if (token.IsCancellationRequested)
    {
        Log($"{PREFIXE_WARN} Annulation demandée — arrêt à l'équation {i + 1}/{equations.Count}.");
        break;
    }

    var eq = equations[i];

    // --- a) Sérialisation JSON du message Kafka ---
    var message = new MessageEquation(
        Source:      "beta",
        EquationId:  eq.EquationId,
        Coefficients: eq.Coefficients,
        Constant:    eq.Constant,
        Timestamp:   DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
    );

    string messageJson;
    try
    {
        messageJson = JsonSerializer.Serialize(message, optionsJson);
    }
    catch (Exception ex)
    {
        Log($"{PREFIXE_ERREUR} Erreur de sérialisation pour {eq.EquationId} : {ex.Message}");
        continue;
    }

    // --- b) Publication sur Kafka ---
    try
    {
        var kafkaMsg = new Message<string, string>
        {
            Key   = eq.EquationId,   // clé = equation_id pour garantir l'ordre par partition
            Value = messageJson,
        };

        // ProduceAsync avec await pour garantir la livraison avant de passer à la suite
        var rapportLivraison = await producteur.ProduceAsync(TOPIC_EQUATIONS, kafkaMsg, token);

        logger.LogDebug("[BETA] Kafka DeliveryReport : topic={Topic} partition={Partition} offset={Offset}",
            rapportLivraison.Topic, rapportLivraison.Partition, rapportLivraison.Offset);
    }
    catch (ProduceException<string, string> ex)
    {
        Log($"{PREFIXE_ERREUR} Échec publication Kafka pour {eq.EquationId} : {ex.Error.Reason}");
        // On continue malgré l'erreur pour ne pas bloquer les équations suivantes
        continue;
    }
    catch (Exception ex)
    {
        Log($"{PREFIXE_ERREUR} Erreur inattendue Kafka pour {eq.EquationId} : {ex.Message}");
        continue;
    }

    // --- c) Marquer dispatched dans PostgreSQL ---
    try
    {
        await using var cmdUpdate = connexionPg.CreateCommand();
        cmdUpdate.CommandText = "UPDATE problem_equations SET dispatched = TRUE WHERE equation_id = @equationId";
        cmdUpdate.Parameters.AddWithValue("equationId", eq.EquationId);
        await cmdUpdate.ExecuteNonQueryAsync(token);
    }
    catch (Exception ex)
    {
        // Non fatal : l'équation a été publiée sur Kafka, on log l'erreur sans interrompre
        Log($"{PREFIXE_ERREUR} Impossible de marquer {eq.EquationId} comme dispatched : {ex.Message}");
    }

    // --- d) Calcul du jitter et de l'intervalle suivant ---
    // Jitter ±10 % calculé avec Random.Shared (thread-safe en .NET 6+)
    double jitterFactor = 1.0 + (Random.Shared.NextDouble() * 0.2 - 0.1);
    int prochainIntervalleMs = (int)(intervalMs * jitterFactor);
    double prochainIntervalleS = prochainIntervalleMs / 1000.0;

    // --- e) Log lisible de l'équation publiée ---
    string coeffsFormates = FormaterCoefficients(eq.Coefficients);
    string prochain = i < equations.Count - 1
        ? $"Prochain: {prochainIntervalleS:F1}s"
        : "Dernière équation";

    Log($"{PREFIXE_BETA} → {eq.EquationId} | {coeffsFormates} = {eq.Constant:F2} | {prochain}");

    // --- f) Attendre l'intervalle avant la prochaine publication ---
    if (i < equations.Count - 1)
    {
        try
        {
            await Task.Delay(prochainIntervalleMs, token);
        }
        catch (TaskCanceledException)
        {
            Log($"{PREFIXE_WARN} Attente interrompue — arrêt propre.");
            break;
        }
    }
}

// ---------------------------------------------------------------------------
// Libération des ressources et confirmation de fin
// ---------------------------------------------------------------------------
// Flush final du producteur Kafka (envoie les messages en attente)
try
{
    producteur.Flush(TimeSpan.FromSeconds(10));
}
catch (Exception ex)
{
    Log($"{PREFIXE_ERREUR} Erreur lors du flush Kafka final : {ex.Message}");
}

producteur.Dispose();
connexionPg.Dispose();

Log($"{PREFIXE_BETA} \u2713 Toutes les équations publiées. Source BETA terminée.");
return 0;

// =============================================================================
// FONCTIONS UTILITAIRES
// =============================================================================

/// <summary>
/// Lit une variable d'environnement et retourne la valeur par défaut si absente.
/// </summary>
static string Env(string nom, string defaut) =>
    Environment.GetEnvironmentVariable(nom) ?? defaut;

/// <summary>
/// Log horodaté vers la sortie standard avec le préfixe BETA.
/// Utilise Console.WriteLine directement pour les couleurs ANSI.
/// </summary>
static void Log(string message)
{
    var horodatage = DateTime.UtcNow.ToString("HH:mm:ss.fff");
    Console.WriteLine($"\x1b[90m{horodatage}\x1b[0m {message}");
}

/// <summary>
/// Formate le dictionnaire de coefficients en une expression algébrique lisible.
/// Ignore les coefficients nuls. Exemple : -2.50*x8 + 1.00*x22 + (-0.75)*x41
/// </summary>
static string FormaterCoefficients(Dictionary<string, double> coefficients)
{
    // On trie les variables par numéro pour un affichage cohérent
    var entrees = coefficients
        .Where(kv => Math.Abs(kv.Value) > 1e-10) // ignorer les coefficients quasi-nuls
        .OrderBy(kv =>
        {
            // Extraction du numéro après "x" pour tri numérique
            if (kv.Key.StartsWith('x') && int.TryParse(kv.Key[1..], out int num))
                return num;
            return int.MaxValue;
        })
        .ToList();

    if (entrees.Count == 0)
        return "0";

    var parties = new List<string>();
    for (int i = 0; i < entrees.Count; i++)
    {
        var (variable, coeff) = (entrees[i].Key, entrees[i].Value);

        if (i == 0)
        {
            // Première partie : afficher le signe et la valeur
            parties.Add(coeff < 0
                ? $"{coeff:F2}*{variable}"
                : $"{coeff:F2}*{variable}");
        }
        else
        {
            // Parties suivantes : séparateur + ou -
            if (coeff >= 0)
                parties.Add($"+ {coeff:F2}*{variable}");
            else
                parties.Add($"+ ({coeff:F2})*{variable}");
        }
    }

    return string.Join(" ", parties);
}

// =============================================================================
// MODÈLES DE DONNÉES
// =============================================================================

/// <summary>
/// Représente une équation chargée depuis PostgreSQL pour la source beta.
/// </summary>
record EquationBeta(
    string EquationId,
    Dictionary<string, double> Coefficients,
    double Constant,
    int DispatchOrder
);

/// <summary>
/// Message JSON publié sur le topic Kafka "equations".
/// Correspond au format attendu par la-chouette-engine.
/// </summary>
record MessageEquation(
    [property: JsonPropertyName("source")]       string Source,
    [property: JsonPropertyName("equation_id")] string EquationId,
    [property: JsonPropertyName("coefficients")] Dictionary<string, double> Coefficients,
    [property: JsonPropertyName("constant")]     double Constant,
    [property: JsonPropertyName("timestamp")]    string Timestamp
);
