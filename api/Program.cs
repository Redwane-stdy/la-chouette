// =============================================================================
// La Chouette — API REST
//
// Expose l'état de l'investigation en temps réel :
//   GET  /api/status             → résumé global de l'investigation
//   GET  /api/variables          → toutes les variables (résolues + inconnues)
//   GET  /api/equations          → équations reçues (paginées)
//   GET  /api/noise              → derniers messages de bruit
//   GET  /api/metrics            → métriques agrégées du pipeline
//   GET  /api/live               → flux SSE temps réel (heartbeat 500ms)
//   POST /api/experiment/reset   → réinitialise l'état sans supprimer le schéma
//   DEL  /api/experiment/purge   → supprime TOUTES les données (irréversible)
// =============================================================================

using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Npgsql;

// ─────────────────────────────────────────────────────────────────────────────
// CONFIGURATION DU BUILDER
// ─────────────────────────────────────────────────────────────────────────────

var builder = WebApplication.CreateBuilder(args);

// ── Sérialisation JSON ──────────────────────────────────────────────────────
// camelCase pour toutes les réponses + ignorer les nulls par défaut
builder.Services.ConfigureHttpJsonOptions(opts =>
{
    opts.SerializerOptions.PropertyNamingPolicy        = JsonNamingPolicy.CamelCase;
    opts.SerializerOptions.DefaultIgnoreCondition      = JsonIgnoreCondition.WhenWritingNull;
    opts.SerializerOptions.WriteIndented               = false;
});

// ── CORS ────────────────────────────────────────────────────────────────────
// Ouvert en développement local — le dashboard tourne sur localhost:3000
builder.Services.AddCors(options =>
{
    options.AddDefaultPolicy(policy =>
    {
        policy
            .WithOrigins("http://localhost:3000")   // dashboard Vite/React
            .AllowAnyHeader()
            .AllowAnyMethod()
            .AllowCredentials();                     // requis pour EventSource SSE avec credentials
    });

    // Politique permissive pour les accès hôte directs
    options.AddPolicy("AnyOrigin", policy =>
        policy.AllowAnyOrigin().AllowAnyHeader().AllowAnyMethod());
});

// ── OpenAPI ─────────────────────────────────────────────────────────────────
builder.Services.AddOpenApi();

// ── Logging coloré ──────────────────────────────────────────────────────────
// Niveau configurable via la variable d'environnement LOG_LEVEL
builder.Logging.ClearProviders();
builder.Logging.AddConsole(opts =>
{
    opts.FormatterName = "simple";
});

// ── Connexion PostgreSQL (Npgsql) ────────────────────────────────────────────
// La chaîne de connexion est fournie par la variable d'environnement POSTGRES_DSN
var postgresDsn = Environment.GetEnvironmentVariable("POSTGRES_DSN")
    ?? "Host=postgres;Port=5432;Database=lachouette;Username=lachouette;Password=secret";

// Convertit le format DSN URL (postgres://user:pwd@host:port/db) vers le format Npgsql
static string NormalizeDsn(string dsn)
{
    if (!dsn.StartsWith("postgres://") && !dsn.StartsWith("postgresql://"))
        return dsn; // déjà au format Npgsql

    // Parsing de l'URL postgres://user:password@host:port/database?params
    var uri = new Uri(dsn);
    var userInfo  = uri.UserInfo.Split(':');
    var user      = Uri.UnescapeDataString(userInfo[0]);
    var password  = userInfo.Length > 1 ? Uri.UnescapeDataString(userInfo[1]) : "";
    var host      = uri.Host;
    var port      = uri.Port > 0 ? uri.Port : 5432;
    var database  = uri.AbsolutePath.TrimStart('/');

    // Gestion du paramètre sslmode dans la query string
    var sslMode = "Disable";
    if (uri.Query.Contains("sslmode=disable", StringComparison.OrdinalIgnoreCase))
        sslMode = "Disable";
    else if (uri.Query.Contains("sslmode=require", StringComparison.OrdinalIgnoreCase))
        sslMode = "Require";

    return $"Host={host};Port={port};Database={database};Username={user};Password={password};SSL Mode={sslMode}";
}

var connectionString = NormalizeDsn(postgresDsn);

// Fabrique de connexion : chaque requête SQL ouvre sa propre connexion (pool Npgsql)
builder.Services.AddSingleton<Func<NpgsqlConnection>>(() =>
{
    var conn = new NpgsqlConnection(connectionString);
    return conn;
});

// ─────────────────────────────────────────────────────────────────────────────
// CONSTRUCTION DE L'APPLICATION
// ─────────────────────────────────────────────────────────────────────────────

var app = builder.Build();

// Niveau de log depuis l'environnement (DEBUG | INFO | WARN | ERROR)
var logLevelEnv = Environment.GetEnvironmentVariable("LOG_LEVEL") ?? "INFO";

// ── Middleware OpenAPI ───────────────────────────────────────────────────────
if (app.Environment.IsDevelopment())
    app.MapOpenApi();

// ── Middleware CORS ──────────────────────────────────────────────────────────
app.UseCors("AnyOrigin");

// ── Middleware de logging des requêtes ──────────────────────────────────────
// Affiche : [API] GET /api/status → 200 (3ms) en jaune ANSI
app.Use(async (context, next) =>
{
    var debut   = DateTime.UtcNow;
    var methode = context.Request.Method;
    var chemin  = context.Request.Path + context.Request.QueryString;

    await next();

    var dureeMs = (int)(DateTime.UtcNow - debut).TotalMilliseconds;
    var code    = context.Response.StatusCode;

    // Couleur selon le code HTTP : vert 2xx, orange 3xx/4xx, rouge 5xx
    var couleurCode = code < 300 ? "\x1b[32m" : code < 500 ? "\x1b[33m" : "\x1b[31m";
    const string jaune  = "\x1b[33m";
    const string reset  = "\x1b[0m";

    Console.WriteLine(
        $"{jaune}[API]{reset} {methode} {chemin} → {couleurCode}{code}{reset} ({dureeMs}ms)"
    );
});

// ── Middleware de gestion globale des erreurs ────────────────────────────────
app.Use(async (context, next) =>
{
    try
    {
        await next();
    }
    catch (NpgsqlException ex)
    {
        LogApi($"Erreur PostgreSQL : {ex.Message}", erreur: true);
        context.Response.StatusCode  = 503;
        context.Response.ContentType = "application/json";
        await context.Response.WriteAsync(JsonSerializer.Serialize(new
        {
            error   = "Base de données indisponible",
            detail  = ex.Message,
            status  = 503
        }));
    }
    catch (Exception ex)
    {
        LogApi($"Erreur interne : {ex.Message}", erreur: true);
        context.Response.StatusCode  = 500;
        context.Response.ContentType = "application/json";
        await context.Response.WriteAsync(JsonSerializer.Serialize(new
        {
            error   = "Erreur interne du serveur",
            detail  = ex.Message,
            status  = 500
        }));
    }
});

// ─────────────────────────────────────────────────────────────────────────────
// HELPERS LOCAUX
// ─────────────────────────────────────────────────────────────────────────────

/// <summary>
/// Affiche un message de log formaté [API] en jaune dans la console.
/// </summary>
static void LogApi(string message, bool erreur = false)
{
    const string jaune  = "\x1b[33m";
    const string rouge  = "\x1b[31m";
    const string reset  = "\x1b[0m";

    var couleur    = erreur ? rouge : jaune;
    var horodatage = DateTime.UtcNow.ToString("yyyy-MM-dd HH:mm:ss");

    Console.WriteLine($"{jaune}[API]{reset} {horodatage} {couleur}{message}{reset}");
}

/// <summary>
/// Ouvre une connexion PostgreSQL fraîche depuis le pool Npgsql.
/// </summary>
static async Task<NpgsqlConnection> OuvrirConnexionAsync(string dsn)
{
    var conn = new NpgsqlConnection(dsn);
    await conn.OpenAsync();
    return conn;
}

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : GET /api/status
// ─────────────────────────────────────────────────────────────────────────────
// Retourne l'état global de l'investigation depuis la vue v_investigation_summary

app.MapGet("/api/status", async () =>
{
    await using var conn = await OuvrirConnexionAsync(connectionString);

    // SQL : lecture de la vue agrégée (une seule ligne — investigation id=1)
    const string sql = """
        SELECT status, equations_received, variables_resolved, variables_total,
               completion_pct, started_at, solved_at, elapsed_seconds,
               noise_messages_received, status_message
        FROM v_investigation_summary;
        """;

    await using var cmd = new NpgsqlCommand(sql, conn);
    await using var rdr = await cmd.ExecuteReaderAsync();

    if (!await rdr.ReadAsync())
        return Results.Problem("Aucune investigation trouvée", statusCode: 404);

    return Results.Ok(new
    {
        status                 = rdr.GetString(0),
        statusMessage          = rdr.GetString(9),
        equationsReceived      = rdr.GetInt32(1),
        variablesResolved      = rdr.GetInt32(2),
        variablesTotal         = rdr.GetInt32(3),
        completionPct          = rdr.GetDouble(4),
        startedAt              = rdr.IsDBNull(5) ? (DateTime?)null : rdr.GetDateTime(5),
        solvedAt               = rdr.IsDBNull(6) ? (DateTime?)null : rdr.GetDateTime(6),
        elapsedSeconds         = rdr.GetInt32(7),
        noiseMessagesReceived  = rdr.GetInt64(8)
    });
})
.WithName("GetStatus")
.WithSummary("État global de l'investigation");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : GET /api/variables
// ─────────────────────────────────────────────────────────────────────────────
// Retourne toutes les variables connues (résolues + inconnues)

app.MapGet("/api/variables", async () =>
{
    await using var conn = await OuvrirConnexionAsync(connectionString);

    // SQL : jointure LEFT entre toutes les variables du problème et celles résolues
    // Les variables non encore résolues apparaissent avec status=UNKNOWN et valeurs null
    const string sql = """
        SELECT
            pv.name,
            rv.variable_name         IS NOT NULL   AS is_resolved,
            rv.computed_value,
            rv.true_value,
            rv.absolute_error,
            rv.resolved_at,
            rv.equations_count
        FROM problem_variables pv
        LEFT JOIN resolved_variables rv ON pv.name = rv.variable_name
        ORDER BY pv.id;
        """;

    await using var cmd  = new NpgsqlCommand(sql, conn);
    await using var rdr  = await cmd.ExecuteReaderAsync();

    var variables = new List<object>();

    while (await rdr.ReadAsync())
    {
        var resolu = rdr.GetBoolean(1);

        if (resolu)
        {
            variables.Add(new
            {
                name                  = rdr.GetString(0),
                status                = "RESOLVED",
                computedValue         = rdr.IsDBNull(2) ? (double?)null : rdr.GetDouble(2),
                trueValue             = rdr.IsDBNull(3) ? (double?)null : rdr.GetDouble(3),
                absoluteError         = rdr.IsDBNull(4) ? (double?)null : rdr.GetDouble(4),
                resolvedAt            = rdr.IsDBNull(5) ? (DateTime?)null : rdr.GetDateTime(5),
                equationsAtResolution = rdr.IsDBNull(6) ? (int?)null    : rdr.GetInt32(6)
            });
        }
        else
        {
            variables.Add(new
            {
                name          = rdr.GetString(0),
                status        = "UNKNOWN",
                computedValue = (double?)null,
                trueValue     = (double?)null,
                absoluteError = (double?)null,
                resolvedAt    = (DateTime?)null,
                equationsAtResolution = (int?)null
            });
        }
    }

    return Results.Ok(variables);
})
.WithName("GetVariables")
.WithSummary("Liste de toutes les variables (résolues + inconnues)");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : GET /api/equations?limit=50&offset=0
// ─────────────────────────────────────────────────────────────────────────────
// Retourne les équations reçues avec pagination

app.MapGet("/api/equations", async (int limit = 50, int offset = 0) =>
{
    // Clamp des paramètres pour éviter les requêtes abusives
    limit  = Math.Clamp(limit,  1, 500);
    offset = Math.Max(offset, 0);

    await using var conn = await OuvrirConnexionAsync(connectionString);

    // SQL : lecture des équations reçues triées par date, avec pagination
    const string sql = """
        SELECT equation_id, source, coefficients::text, constant, received_at
        FROM equations_received
        ORDER BY received_at DESC
        LIMIT $1 OFFSET $2;
        """;

    await using var cmd = new NpgsqlCommand(sql, conn);
    cmd.Parameters.AddWithValue(limit);
    cmd.Parameters.AddWithValue(offset);

    await using var rdr    = await cmd.ExecuteReaderAsync();
    var equations          = new List<object>();

    while (await rdr.ReadAsync())
    {
        // Désérialisation des coefficients JSONB → dictionnaire
        var coefficientsJson = rdr.GetString(2);
        Dictionary<string, double>? coefficients = null;
        try
        {
            coefficients = JsonSerializer.Deserialize<Dictionary<string, double>>(coefficientsJson);
        }
        catch
        {
            // Si la désérialisation échoue, on retourne le JSON brut sous forme de string
            coefficients = null;
        }

        equations.Add(new
        {
            equationId   = rdr.GetString(0),
            source       = rdr.GetString(1),
            coefficients = (object?)coefficients ?? coefficientsJson,
            constant     = rdr.GetDouble(3),
            receivedAt   = rdr.GetDateTime(4)
        });
    }

    return Results.Ok(equations);
})
.WithName("GetEquations")
.WithSummary("Équations reçues (paginées)");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : GET /api/noise?limit=20
// ─────────────────────────────────────────────────────────────────────────────
// Retourne les derniers messages de bruit reçus

app.MapGet("/api/noise", async (int limit = 20) =>
{
    limit = Math.Clamp(limit, 1, 200);

    await using var conn = await OuvrirConnexionAsync(connectionString);

    // SQL : N derniers messages de bruit triés du plus récent au plus ancien
    const string sql = """
        SELECT source, headline, received_at
        FROM noise_log
        ORDER BY received_at DESC
        LIMIT $1;
        """;

    await using var cmd = new NpgsqlCommand(sql, conn);
    cmd.Parameters.AddWithValue(limit);

    await using var rdr  = await cmd.ExecuteReaderAsync();
    var messages         = new List<object>();

    while (await rdr.ReadAsync())
    {
        messages.Add(new
        {
            source     = rdr.GetString(0),
            text       = rdr.GetString(1),
            receivedAt = rdr.GetDateTime(2)
        });
    }

    return Results.Ok(messages);
})
.WithName("GetNoise")
.WithSummary("Derniers messages de bruit");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : GET /api/metrics
// ─────────────────────────────────────────────────────────────────────────────
// Métriques agrégées : débit, taux de résolution, latence

app.MapGet("/api/metrics", async () =>
{
    await using var conn = await OuvrirConnexionAsync(connectionString);

    // SQL : état global pour calculer les métriques de débit
    const string sqlEtat = """
        SELECT equations_received, variables_resolved, variables_total,
               completion_pct, elapsed_seconds,
               (SELECT COUNT(*) FROM noise_log) AS total_bruit
        FROM investigation_state
        WHERE id = 1;
        """;

    await using var cmd = new NpgsqlCommand(sqlEtat, conn);
    await using var rdr = await cmd.ExecuteReaderAsync();

    if (!await rdr.ReadAsync())
        return Results.Problem("État introuvable", statusCode: 404);

    var equationsRecues    = rdr.GetInt32(0);
    var variablesResolues  = rdr.GetInt32(1);
    var variablesTotal     = rdr.GetInt32(2);
    var completionPct      = rdr.GetDouble(3);
    var elapsedSeconds     = rdr.GetInt32(4);
    var totalBruit         = rdr.GetInt64(5);

    // Calcul du débit : équations / seconde et équations / minute
    var equationsParSeconde = elapsedSeconds > 0
        ? Math.Round((double)equationsRecues / elapsedSeconds, 2)
        : 0.0;

    var equationsParMinute = elapsedSeconds > 0
        ? Math.Round((double)equationsRecues / elapsedSeconds * 60.0, 1)
        : 0.0;

    return Results.Ok(new
    {
        equationsPerSecond     = equationsParSeconde,
        variablesResolved      = variablesResolues,
        variablesTotal         = variablesTotal,
        completionPct          = completionPct,
        totalNoise             = totalBruit,
        uptimeSeconds          = elapsedSeconds,
        avgEquationsPerMinute  = equationsParMinute
    });
})
.WithName("GetMetrics")
.WithSummary("Métriques agrégées du pipeline");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : GET /api/live  (Server-Sent Events)
// ─────────────────────────────────────────────────────────────────────────────
// Flux SSE : envoie l'état courant toutes les 500ms.
// Le client peut écouter avec EventSource('http://localhost:8080/api/live').

app.MapGet("/api/live", async (HttpContext ctx) =>
{
    // En-têtes SSE obligatoires
    ctx.Response.Headers.Append("Content-Type",  "text/event-stream");
    ctx.Response.Headers.Append("Cache-Control", "no-cache");
    ctx.Response.Headers.Append("Connection",    "keep-alive");
    ctx.Response.Headers.Append("X-Accel-Buffering", "no"); // désactive le buffering nginx

    await ctx.Response.Body.FlushAsync();

    LogApi("Nouveau client SSE connecté — flux /api/live démarré");

    var annulation = ctx.RequestAborted;
    var compteur   = 0;

    while (!annulation.IsCancellationRequested)
    {
        try
        {
            // SQL : snapshot de l'état courant pour le heartbeat SSE
            const string sqlSnapshot = """
                SELECT status, equations_received, variables_resolved,
                       variables_total, completion_pct, elapsed_seconds,
                       (SELECT COUNT(*) FROM noise_log) AS bruit,
                       status_message
                FROM v_investigation_summary;
                """;

            await using var conn = await OuvrirConnexionAsync(connectionString);
            await using var cmd  = new NpgsqlCommand(sqlSnapshot, conn);
            await using var rdr  = await cmd.ExecuteReaderAsync();

            string payload;

            if (await rdr.ReadAsync())
            {
                var snapshot = new
                {
                    status            = rdr.GetString(0),
                    equationsReceived = rdr.GetInt32(1),
                    variablesResolved = rdr.GetInt32(2),
                    variablesTotal    = rdr.GetInt32(3),
                    completionPct     = rdr.GetDouble(4),
                    elapsedSeconds    = rdr.GetInt32(5),
                    totalNoise        = rdr.GetInt64(6),
                    statusMessage     = rdr.GetString(7),
                    heartbeat         = compteur
                };
                payload = JsonSerializer.Serialize(snapshot, new JsonSerializerOptions
                {
                    PropertyNamingPolicy = JsonNamingPolicy.CamelCase
                });
            }
            else
            {
                // Heartbeat minimal si la BDD ne répond pas encore
                payload = $"{{\"heartbeat\":{compteur},\"status\":\"INITIALIZING\"}}";
            }

            // Format SSE : "data: <json>\n\n"
            var message = $"data: {payload}\n\n";
            var octets  = Encoding.UTF8.GetBytes(message);

            await ctx.Response.Body.WriteAsync(octets, annulation);
            await ctx.Response.Body.FlushAsync(annulation);

            compteur++;
        }
        catch (OperationCanceledException)
        {
            // Le client s'est déconnecté — sortie propre de la boucle
            break;
        }
        catch (NpgsqlException ex)
        {
            // La BDD est momentanément indisponible — on envoie un heartbeat d'erreur
            LogApi($"SSE — Erreur BDD : {ex.Message}", erreur: true);
            var errPayload = $"data: {{\"heartbeat\":{compteur},\"status\":\"DB_ERROR\",\"error\":\"{ex.Message.Replace("\"", "\\\"")}\"}}\n\n";
            try
            {
                await ctx.Response.Body.WriteAsync(Encoding.UTF8.GetBytes(errPayload), annulation);
                await ctx.Response.Body.FlushAsync(annulation);
            }
            catch { break; }
        }

        // Pause de 500ms entre chaque heartbeat
        try
        {
            await Task.Delay(500, annulation);
        }
        catch (OperationCanceledException)
        {
            break;
        }
    }

    LogApi("Client SSE déconnecté — flux /api/live terminé");
})
.WithName("LiveStream")
.WithSummary("Flux SSE temps réel (heartbeat 500ms)");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : POST /api/experiment/reset
// ─────────────────────────────────────────────────────────────────────────────
// Vide les données de l'expérience et remet l'état initial
// Body attendu : { "confirm": true }

app.MapPost("/api/experiment/reset", async (HttpContext ctx) =>
{
    // Lecture et validation du body
    ResetBody? body;
    try
    {
        body = await ctx.Request.ReadFromJsonAsync<ResetBody>();
    }
    catch
    {
        return Results.BadRequest(new { error = "Corps JSON invalide — attendu : {\"confirm\": true}" });
    }

    if (body is null || !body.Confirm)
        return Results.BadRequest(new { error = "Confirmation requise — envoyez {\"confirm\": true}" });

    var horodatage = DateTime.UtcNow.ToString("yyyy-MM-dd HH:mm:ss UTC");
    LogApi($"RESET demandé à {horodatage} — vidage des tables en cours...");

    await using var conn = await OuvrirConnexionAsync(connectionString);
    await using var tx   = await conn.BeginTransactionAsync();

    try
    {
        // SQL : suppression des données transitoires (pas le schéma ni les variables du problème)
        const string sqlReset = """
            TRUNCATE TABLE equations_received RESTART IDENTITY CASCADE;
            TRUNCATE TABLE resolved_variables  RESTART IDENTITY CASCADE;
            TRUNCATE TABLE noise_log           RESTART IDENTITY CASCADE;

            UPDATE investigation_state SET
                status              = 'COLLECTING',
                equations_received  = 0,
                variables_resolved  = 0,
                completion_pct      = 0.0,
                experiment_started  = FALSE,
                started_at          = NOW(),
                solved_at           = NULL,
                updated_at          = NOW()
            WHERE id = 1;
            """;

        await using var cmd = new NpgsqlCommand(sqlReset, conn, tx);
        await cmd.ExecuteNonQueryAsync();
        await tx.CommitAsync();

        LogApi($"RESET terminé à {DateTime.UtcNow:yyyy-MM-dd HH:mm:ss UTC} — état remis à COLLECTING");

        return Results.Ok(new
        {
            success   = true,
            message   = "État réinitialisé avec succès",
            resetAt   = DateTime.UtcNow
        });
    }
    catch (Exception ex)
    {
        await tx.RollbackAsync();
        LogApi($"RESET échoué : {ex.Message}", erreur: true);
        return Results.Problem(
            detail:     ex.Message,
            title:      "Échec de la réinitialisation",
            statusCode: 500
        );
    }
})
.WithName("ResetExperiment")
.WithSummary("Réinitialise l'investigation (vide les données, conserve le schéma)");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : DELETE /api/experiment/purge
// ─────────────────────────────────────────────────────────────────────────────
// Supprime TOUTES les données y compris problem_variables et problem_equations
// Body attendu : { "confirm": true, "deleteEverything": true }

app.MapDelete("/api/experiment/purge", async (HttpContext ctx) =>
{
    // Lecture et validation du body
    PurgeBody? body;
    try
    {
        body = await ctx.Request.ReadFromJsonAsync<PurgeBody>();
    }
    catch
    {
        return Results.BadRequest(new { error = "Corps JSON invalide" });
    }

    if (body is null || !body.Confirm || !body.DeleteEverything)
        return Results.BadRequest(new
        {
            error = "Double confirmation requise — envoyez {\"confirm\": true, \"deleteEverything\": true}"
        });

    var horodatage = DateTime.UtcNow.ToString("yyyy-MM-dd HH:mm:ss UTC");
    LogApi($"PURGE TOTALE demandée à {horodatage} — suppression de TOUTES les données...", erreur: true);

    await using var conn = await OuvrirConnexionAsync(connectionString);
    await using var tx   = await conn.BeginTransactionAsync();

    try
    {
        // SQL : suppression intégrale — TOUTES les tables vidées + séquences réinitialisées
        // ATTENTION : opération irréversible — problem_variables et problem_equations inclus
        const string sqlPurge = """
            TRUNCATE TABLE equations_received  RESTART IDENTITY CASCADE;
            TRUNCATE TABLE resolved_variables   RESTART IDENTITY CASCADE;
            TRUNCATE TABLE noise_log            RESTART IDENTITY CASCADE;
            TRUNCATE TABLE problem_equations    RESTART IDENTITY CASCADE;
            TRUNCATE TABLE problem_variables    RESTART IDENTITY CASCADE;

            UPDATE investigation_state SET
                status              = 'COLLECTING',
                equations_received  = 0,
                variables_resolved  = 0,
                variables_total     = 50,
                completion_pct      = 0.0,
                experiment_started  = FALSE,
                started_at          = NOW(),
                solved_at           = NULL,
                updated_at          = NOW()
            WHERE id = 1;
            """;

        await using var cmd = new NpgsqlCommand(sqlPurge, conn, tx);
        await cmd.ExecuteNonQueryAsync();
        await tx.CommitAsync();

        LogApi($"PURGE TOTALE terminée à {DateTime.UtcNow:yyyy-MM-dd HH:mm:ss UTC} — base vide", erreur: true);

        return Results.Ok(new
        {
            success  = true,
            message  = "Toutes les données supprimées",
            purgedAt = DateTime.UtcNow
        });
    }
    catch (Exception ex)
    {
        await tx.RollbackAsync();
        LogApi($"PURGE échouée : {ex.Message}", erreur: true);
        return Results.Problem(
            detail:     ex.Message,
            title:      "Échec de la purge",
            statusCode: 500
        );
    }
})
.WithName("PurgeExperiment")
.WithSummary("Supprime TOUTES les données (irréversible — y compris le problème)");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : GET /api/experiment/ready
// ─────────────────────────────────────────────────────────────────────────────
// Retourne {"ready": true} si l'expérience a été démarrée, false sinon

app.MapGet("/api/experiment/ready", async () =>
{
    await using var conn = await OuvrirConnexionAsync(connectionString);

    await using var cmd = new NpgsqlCommand(
        "SELECT experiment_started FROM investigation_state WHERE id = 1", conn);
    var result = await cmd.ExecuteScalarAsync();
    bool started = result is bool b && b;

    return Results.Ok(new { ready = started });
})
.RequireCors("AnyOrigin")
.WithName("GetExperimentReady")
.WithSummary("Indique si l'expérience a été démarrée");

// ─────────────────────────────────────────────────────────────────────────────
// ENDPOINT : POST /api/experiment/start
// ─────────────────────────────────────────────────────────────────────────────
// Démarre l'expérience : les sources commencent à publier des équations

app.MapPost("/api/experiment/start", async () =>
{
    await using var conn = await OuvrirConnexionAsync(connectionString);

    await using var cmd = new NpgsqlCommand(
        "UPDATE investigation_state SET experiment_started = TRUE WHERE id = 1", conn);
    await cmd.ExecuteNonQueryAsync();

    LogApi("Expérience démarrée — experiment_started = TRUE");

    return Results.Ok(new { started = true, message = "Expérience démarrée — les sources commencent à publier." });
})
.RequireCors("AnyOrigin")
.WithName("StartExperiment")
.WithSummary("Démarre l'expérience — les sources commencent à publier");

// ─────────────────────────────────────────────────────────────────────────────
// DÉMARRAGE
// ─────────────────────────────────────────────────────────────────────────────

// Message de démarrage en jaune ANSI
LogApi("═══════════════════════════════════════════════");
LogApi("  La Chouette — API REST démarrée");
LogApi($"  PostgreSQL DSN : {postgresDsn[..Math.Min(40, postgresDsn.Length)]}...");
LogApi($"  Log level      : {logLevelEnv}");
LogApi("  Endpoints disponibles :");
LogApi("    GET  /api/status                → état global");
LogApi("    GET  /api/variables             → toutes les variables");
LogApi("    GET  /api/equations             → équations reçues");
LogApi("    GET  /api/noise                 → messages de bruit");
LogApi("    GET  /api/metrics               → métriques agrégées");
LogApi("    GET  /api/live                  → flux SSE (500ms)");
LogApi("    GET  /api/experiment/ready      → expérience démarrée ?");
LogApi("    POST /api/experiment/start      → démarre l'expérience");
LogApi("    POST /api/experiment/reset      → réinitialise l'état");
LogApi("    DEL  /api/experiment/purge      → purge totale (danger)");
LogApi("═══════════════════════════════════════════════");

app.Run();

// ─────────────────────────────────────────────────────────────────────────────
// RECORDS — corps des requêtes de mutation
// ─────────────────────────────────────────────────────────────────────────────

/// <summary>
/// Corps attendu pour POST /api/experiment/reset
/// </summary>
record ResetBody(
    [property: JsonPropertyName("confirm")]
    bool Confirm
);

/// <summary>
/// Corps attendu pour DELETE /api/experiment/purge
/// Double confirmation obligatoire pour éviter les suppressions accidentelles.
/// </summary>
record PurgeBody(
    [property: JsonPropertyName("confirm")]
    bool Confirm,

    [property: JsonPropertyName("deleteEverything")]
    bool DeleteEverything
);
