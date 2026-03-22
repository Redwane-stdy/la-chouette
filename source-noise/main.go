// source-noise — Générateur de bruit Kafka pour le projet La Chouette.
//
// Ce service injecte en continu des messages de "bruit" sur le topic Kafka "noise".
// Ces messages simulent un flux de news économiques et financières provenant de
// sources variées (Reuters, BFM, Bloomberg, Twitter), permettant au moteur
// La Chouette de tester sa résistance aux signaux parasites.
//
// Comportement :
//  1. Connexion au broker Kafka avec retry automatique (toutes les 5s, max 20 essais).
//  2. Boucle infinie : choisit une headline aléatoire parmi 30 prédéfinies,
//     choisit une source aléatoire parmi 4, publie sur le topic "noise",
//     attend un intervalle aléatoire entre SOURCE_NOISE_INTERVAL_MIN_MS et MAX.
//  3. Le service tourne indéfiniment jusqu'à réception d'un SIGTERM/SIGINT.
//
// Variables d'environnement :
//   - KAFKA_BROKERS                : adresse(s) du broker Kafka (défaut: kafka:9092)
//   - SOURCE_NOISE_INTERVAL_MIN_MS : intervalle minimum entre publications en ms (défaut: 2000)
//   - SOURCE_NOISE_INTERVAL_MAX_MS : intervalle maximum entre publications en ms (défaut: 7000)
//   - DEMO_MODE                    : si "true", divise les intervalles par 3
//   - LOG_LEVEL                    : DEBUG | INFO | WARN | ERROR (défaut: INFO)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	kafka "github.com/segmentio/kafka-go"
)

// ─────────────────────────────────────────────────────────────────────────────
// Structures de données
// ─────────────────────────────────────────────────────────────────────────────

// NoiseHeadline représente une accroche de news avec son corps de texte.
type NoiseHeadline struct {
	Headline string // titre de l'article
	Body     string // résumé du contenu
}

// KafkaNoiseMessage est la structure JSON publiée sur le topic "noise".
type KafkaNoiseMessage struct {
	Source    string `json:"source"`    // ex: "noise_reuters"
	Type      string `json:"type"`      // toujours "news" dans cette implémentation
	Headline  string `json:"headline"`  // titre de l'article
	Body      string `json:"body"`      // résumé du contenu
	Timestamp string `json:"timestamp"` // ISO 8601 UTC
}

// ─────────────────────────────────────────────────────────────────────────────
// Configuration
// ─────────────────────────────────────────────────────────────────────────────

// Config regroupe tous les paramètres de configuration du générateur de bruit.
type Config struct {
	KafkaBrokers  string // adresses broker séparées par des virgules
	IntervalMinMs int64  // intervalle minimum entre publications (ms)
	IntervalMaxMs int64  // intervalle maximum entre publications (ms)
	DemoMode      bool   // mode démo : divise les intervalles par 3
	LogLevel      string // niveau de log
	KafkaTopic    string // topic cible, "noise"
}

// loadConfig lit les variables d'environnement et retourne la configuration.
func loadConfig() Config {
	// Intervalle minimum : défaut 2 secondes
	minMs := int64(2000)
	if v := os.Getenv("SOURCE_NOISE_INTERVAL_MIN_MS"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			minMs = parsed
		}
	}

	// Intervalle maximum : défaut 7 secondes
	maxMs := int64(7000)
	if v := os.Getenv("SOURCE_NOISE_INTERVAL_MAX_MS"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			maxMs = parsed
		}
	}

	// Validation : min doit être inférieur à max
	if minMs >= maxMs {
		minMs = maxMs / 2
	}

	demoMode := strings.ToLower(os.Getenv("DEMO_MODE")) == "true"
	if demoMode {
		// En mode démo, on accélère le flux de bruit pour remplir rapidement le pipeline
		minMs = minMs / 3
		maxMs = maxMs / 3
	}

	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		brokers = "kafka:9092"
	}

	return Config{
		KafkaBrokers:  brokers,
		IntervalMinMs: minMs,
		IntervalMaxMs: maxMs,
		DemoMode:      demoMode,
		LogLevel:      os.Getenv("LOG_LEVEL"),
		KafkaTopic:    "noise",
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Logging — couleur GRISE pour [NOISE]
// ─────────────────────────────────────────────────────────────────────────────

// Codes ANSI pour le gris (bright black) et le reset.
const (
	colorGray  = "\033[90m" // ANSI bright black — apparaît gris dans la plupart des terminaux
	colorReset = "\033[0m"
)

// setupLogger configure zerolog avec le niveau demandé et une sortie console
// colorisée. Le préfixe [NOISE] est injecté en gris dans chaque message.
func setupLogger(level string) {
	output := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02T15:04:05.000Z",
		// Ajout du préfixe coloré [NOISE] au formatage du message
		FormatMessage: func(i interface{}) string {
			return fmt.Sprintf("%s[NOISE]%s %s", colorGray, colorReset, i)
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
// Données — 30 headlines prédéfinies en français
// ─────────────────────────────────────────────────────────────────────────────

// headlines contient 30 titres de news économiques, financières et tech variées.
// Ces données sont hardcodées dans le binaire : pas besoin de base de données.
var headlines = []NoiseHeadline{
	// ── Banques centrales & politique monétaire ──────────────────────────────
	{
		Headline: "La Fed maintient ses taux directeurs à 5.25% malgré les pressions inflationnistes",
		Body:     "Lors de sa réunion du FOMC, la Réserve fédérale américaine a voté à l'unanimité pour maintenir les taux entre 5.00% et 5.25%, signalant une approche prudente face à une inflation encore au-dessus de l'objectif de 2%.",
	},
	{
		Headline: "La BCE relève ses taux de 25 points de base dans un contexte d'inflation persistante",
		Body:     "Christine Lagarde a annoncé une nouvelle hausse des taux directeurs de la Banque centrale européenne à 4.50%, soulignant que la lutte contre l'inflation reste la priorité absolue pour 2024.",
	},
	{
		Headline: "La Banque d'Angleterre surprend les marchés avec une pause dans son cycle de resserrement",
		Body:     "Contre toutes les attentes, le Monetary Policy Committee a voté 5-4 pour maintenir les taux à 5.25%, invoquant un ralentissement des indicateurs d'activité économique au Royaume-Uni.",
	},
	// ── Marchés financiers & indices boursiers ───────────────────────────────
	{
		Headline: "Le CAC 40 clôture en hausse de 0.8% porté par les valeurs du luxe",
		Body:     "LVMH, Hermès et Kering ont tiré l'indice parisien vers le haut, portés par des résultats trimestriels supérieurs aux attentes des analystes. Le CAC franchit à nouveau le seuil des 7 500 points.",
	},
	{
		Headline: "Wall Street ouvre en forte baisse suite à des chiffres d'emploi décevants",
		Body:     "Le rapport mensuel sur l'emploi américain (NFP) montre une création de seulement 120 000 postes contre 185 000 attendus, alimentant les craintes d'un ralentissement de la croissance aux États-Unis.",
	},
	{
		Headline: "Le Nikkei 225 atteint un plus haut historique depuis 1989 grâce à la faiblesse du yen",
		Body:     "L'indice phare de la Bourse de Tokyo a bondi de 2.3% pour atteindre 38 950 points, niveau inédit depuis la bulle spéculative japonaise des années 1980. La faiblesse du yen profite aux exportateurs.",
	},
	{
		Headline: "Les actions des semi-conducteurs s'envolent après les résultats records de NVIDIA",
		Body:     "NVIDIA a publié des revenus trimestriels de 22,1 milliards de dollars, dépassant de 38% les estimations de Wall Street. La demande pour ses puces IA H100 reste massivement supérieure à l'offre disponible.",
	},
	{
		Headline: "Forte volatilité sur les marchés émergents en raison de la vigueur du dollar",
		Body:     "L'indice MSCI Emerging Markets recule de 1.5% tandis que le dollar américain atteint son plus haut niveau depuis six mois face à un panier de devises. Les flux de capitaux quittent massivement l'Asie du Sud-Est.",
	},
	// ── Technologie & IA ─────────────────────────────────────────────────────
	{
		Headline: "Apple présente l'iPhone 17 avec une autonomie record et une puce A19 révolutionnaire",
		Body:     "Lors de sa keynote à Cupertino, Tim Cook a dévoilé l'iPhone 17 doté d'une autonomie de 32 heures en utilisation mixte, d'un écran micro-OLED 4K et d'un processeur A19 fabriqué en 2nm par TSMC.",
	},
	{
		Headline: "OpenAI valorisé à 80 milliards de dollars lors de sa dernière levée de fonds",
		Body:     "OpenAI a finalisé un tour de table de 10 milliards de dollars impliquant Microsoft, Thrive Capital et plusieurs fonds souverains du Moyen-Orient. La société envisage une introduction en bourse pour 2025.",
	},
	{
		Headline: "Google DeepMind annonce une percée majeure dans la prédiction des structures protéiques",
		Body:     "AlphaFold 3, la nouvelle version du système de prédiction de DeepMind, atteint une précision de 98.5% sur des protéines inconnues, ouvrant la voie à une révolution dans la découverte de médicaments.",
	},
	{
		Headline: "Le Congrès américain adopte un projet de loi encadrant l'utilisation de l'IA générative",
		Body:     "La loi AI Transparency Act impose aux développeurs de modèles de plus de 10 milliards de paramètres de publier des audits de sécurité trimestriels et de déclarer les données d'entraînement utilisées.",
	},
	{
		Headline: "Tesla lance sa flotte de robotaxis à San Francisco après deux ans de retard",
		Body:     "Elon Musk a officiellement inauguré le service Cybercab dans la Bay Area avec une flotte initiale de 500 véhicules autonomes de niveau 4. Le prix de la course est fixé à 0.35 dollar par mile.",
	},
	// ── Energie & matières premières ─────────────────────────────────────────
	{
		Headline: "Le pétrole Brent dépasse 95 dollars le baril après les coupes de production de l'OPEP+",
		Body:     "L'Arabie Saoudite et la Russie ont confirmé la prolongation de leurs réductions volontaires de production jusqu'à fin 2024, propulsant les cours du brut à leur plus haut niveau depuis quatorze mois.",
	},
	{
		Headline: "Les prix du gaz naturel en Europe s'effondrent grâce aux stocks hivernaux au maximum",
		Body:     "Les stocks de gaz naturel européens sont remplis à 95% de leur capacité avant l'hiver, réduisant la pression sur les prix qui reviennent sous la barre des 35 euros par MWh pour la première fois depuis 2021.",
	},
	{
		Headline: "L'or franchit le cap symbolique des 2 100 dollars l'once pour la première fois",
		Body:     "Le métal précieux atteint un nouveau record historique, porté par les achats massifs des banques centrales émergentes et la demande des investisseurs cherchant à se protéger de l'inflation persistante.",
	},
	{
		Headline: "Le lithium plonge de 40% en six mois face à la surcapacité de production en Chine",
		Body:     "Les prix du carbonate de lithium ont chuté drastiquement, mettant sous pression les producteurs australiens et chiliens. Les fabricants de batteries pour véhicules électriques pourraient en revanche bénéficier de cette baisse.",
	},
	// ── Macro-économie & géopolitique ────────────────────────────────────────
	{
		Headline: "L'inflation en zone euro ralentit à 2.4% en décembre, plus proche de l'objectif BCE",
		Body:     "L'indice des prix à la consommation harmonisé (IPCH) de la zone euro affiche sa plus faible progression depuis deux ans, grâce à la baisse des prix de l'énergie et à l'effet des hausses de taux de la BCE.",
	},
	{
		Headline: "La Chine annonce un plan de relance de 1 000 milliards de yuans pour soutenir son économie",
		Body:     "Pékin dévoile un programme d'investissements massifs dans les infrastructures, la technologie verte et le logement, cherchant à compenser le ralentissement du secteur immobilier et la faiblesse de la consommation intérieure.",
	},
	{
		Headline: "Le FMI relève ses prévisions de croissance mondiale à 3.2% pour 2024",
		Body:     "Dans ses dernières Perspectives de l'économie mondiale, le FMI note une résilience inattendue des économies avancées et un rebond des marchés émergents hors Chine, malgré les tensions géopolitiques persistantes.",
	},
	{
		Headline: "Récession technique en Allemagne : le PIB allemand recule pour le deuxième trimestre consécutif",
		Body:     "L'économie allemande, première d'Europe, affiche une contraction de 0.3% au T4 2023 après -0.1% au T3. La faiblesse de l'industrie manufacturière et la crise énergétique pèsent lourdement sur la locomotive européenne.",
	},
	// ── Crypto-actifs & finance décentralisée ────────────────────────────────
	{
		Headline: "Le Bitcoin dépasse 65 000 dollars après l'approbation des ETF spot par la SEC",
		Body:     "La Securities and Exchange Commission américaine a validé onze ETF Bitcoin au comptant, permettant aux investisseurs institutionnels d'accéder au crypto-actif via des véhicules d'investissement traditionnels réglementés.",
	},
	{
		Headline: "Ethereum franchit la barre des 4 000 dollars dans l'attente de l'upgrade Dencun",
		Body:     "La mise à niveau Dencun du réseau Ethereum, prévue pour le T1 2024, promet de réduire drastiquement les frais de transaction (gas fees) sur les Layer 2, stimulant l'intérêt des développeurs DeFi.",
	},
	{
		Headline: "La SEC engage des poursuites judiciaires contre Binance pour manipulation de marché",
		Body:     "Le gendarme boursier américain accuse la plus grande plateforme d'échange de crypto-monnaies de gonfler artificiellement ses volumes de trading et de mélanger les fonds des clients avec ceux de la société.",
	},
	// ── Commerce international & chaînes d'approvisionnement ─────────────────
	{
		Headline: "Les États-Unis imposent de nouveaux droits de douane de 60% sur les véhicules électriques chinois",
		Body:     "L'administration Biden multiplie par quatre les tarifs sur les voitures électriques fabriquées en Chine, cherchant à protéger l'industrie automobile américaine naissante face à la concurrence de BYD, NIO et SAIC.",
	},
	{
		Headline: "Les perturbations en mer Rouge font exploser les coûts du fret maritime mondial",
		Body:     "Les attaques des Houthis contre des navires commerciaux en mer Rouge contraignent les armateurs à contourner l'Afrique via le Cap de Bonne-Espérance, allongeant les délais de 14 jours et triplant les tarifs spot.",
	},
	// ── Fusions-acquisitions & marchés primaires ──────────────────────────────
	{
		Headline: "Microsoft finalise le rachat d'Activision Blizzard pour 69 milliards de dollars",
		Body:     "Après 21 mois de procédures réglementaires et de recours judiciaires dans plusieurs pays, la plus grande acquisition de l'histoire du jeu vidéo est officiellement bouclée. Xbox hérite désormais de Call of Duty, Warcraft et Candy Crush.",
	},
	{
		Headline: "ARM Holdings s'envole de 25% lors de son introduction en Bourse au Nasdaq",
		Body:     "La société britannique de conception de puces, dont SoftBank détient encore 90%, lève 4,87 milliards de dollars dans le cadre de la plus grande IPO technologique depuis deux ans, avec un cours d'ouverture à 56.10 dollars.",
	},
	// ── Immobilier & taux hypothécaires ──────────────────────────────────────
	{
		Headline: "Les taux immobiliers américains atteignent 8%, plus haut niveau depuis 2000",
		Body:     "Le taux moyen du crédit immobilier sur 30 ans aux États-Unis franchit le seuil symbolique de 8% selon Freddie Mac, réduisant drastiquement le pouvoir d'achat des ménages et déprimant les ventes de logements existants.",
	},
	// ── Banques & secteur financier ───────────────────────────────────────────
	{
		Headline: "JPMorgan Chase publie un bénéfice record de 49 milliards de dollars pour 2023",
		Body:     "La première banque américaine profite à plein de la hausse des taux d'intérêt qui gonfle ses marges nettes d'intérêts. Jamie Dimon avertit néanmoins d'un ralentissement attendu pour 2024 dans un contexte de normalisation monétaire.",
	},
}

// noiseSources est la liste des sources fictives qui simulent différents canaux d'information.
// Chaque publication choisit aléatoirement parmi ces sources pour varier le signal.
var noiseSources = []string{
	"noise_reuters",
	"noise_bfm",
	"noise_bloomberg",
	"noise_twitter",
}

// ─────────────────────────────────────────────────────────────────────────────
// Connexion Kafka — avec retry
// ─────────────────────────────────────────────────────────────────────────────

// connectKafka crée un writer Kafka avec retry automatique intégré.
// Effectue une validation de connexion avant de retourner le writer.
func connectKafka(ctx context.Context, brokers string, topic string) (*kafka.Writer, error) {
	brokerList := strings.Split(brokers, ",")

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(brokerList...),
		Topic:                  topic,
		Balancer:               &kafka.LeastBytes{},
		// Retry automatique en cas de déconnexion transitoire du broker
		MaxAttempts:            10,
		RequiredAcks:           kafka.RequireOne,
		Async:                  false, // synchrone pour garantir la livraison avant de continuer
		AllowAutoTopicCreation: false,
	}

	// Vérification de la joignabilité du broker avant de démarrer la boucle principale
	maxAttempts := 20
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			writer.Close()
			return nil, ctx.Err()
		default:
		}

		conn, dialErr := kafka.DialContext(ctx, "tcp", brokerList[0])
		if dialErr == nil {
			conn.Close()
			log.Info().Msg("Connexion Kafka établie")
			return writer, nil
		}
		lastErr = dialErr

		log.Warn().Err(dialErr).Msgf("Kafka non disponible, tentative %d/%d, nouvelle tentative dans 5s...", attempt, maxAttempts)

		select {
		case <-ctx.Done():
			writer.Close()
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}

	writer.Close()
	return nil, fmt.Errorf("impossible de joindre Kafka après %d tentatives : %w", maxAttempts, lastErr)
}

// ─────────────────────────────────────────────────────────────────────────────
// Publication du bruit
// ─────────────────────────────────────────────────────────────────────────────

// publishNoise construit un message de bruit aléatoire et le publie sur le topic Kafka.
// La source et la headline sont choisies aléatoirement à chaque appel.
func publishNoise(ctx context.Context, writer *kafka.Writer) (string, string, error) {
	// Sélection aléatoire d'une headline parmi les 30 disponibles
	selected := headlines[rand.Intn(len(headlines))]

	// Sélection aléatoire d'une source parmi les 4 disponibles
	source := noiseSources[rand.Intn(len(noiseSources))]

	// Construction du message JSON
	msg := KafkaNoiseMessage{
		Source:    source,
		Type:      "news",
		Headline:  selected.Headline,
		Body:      selected.Body,
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return "", "", fmt.Errorf("erreur sérialisation message de bruit : %w", err)
	}

	kafkaMsg := kafka.Message{
		// Pas de clé fixe : on laisse kafka-go distribuer les messages uniformément
		Value: payload,
	}

	if err := writer.WriteMessages(ctx, kafkaMsg); err != nil {
		return "", "", fmt.Errorf("erreur publication Kafka : %w", err)
	}

	// Extraction du nom court de la source pour les logs (ex: "reuters" depuis "noise_reuters")
	shortSource := strings.TrimPrefix(source, "noise_")
	return shortSource, selected.Headline, nil
}

// randomInterval génère un intervalle aléatoire entre minMs et maxMs millisecondes.
func randomInterval(minMs, maxMs int64) time.Duration {
	// rand.Int63n(max-min) + min garantit une valeur dans [minMs, maxMs)
	rangeMs := maxMs - minMs
	return time.Duration(minMs+rand.Int63n(rangeMs)) * time.Millisecond
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
		Int64("interval_min_ms", cfg.IntervalMinMs).
		Int64("interval_max_ms", cfg.IntervalMaxMs).
		Bool("demo_mode", cfg.DemoMode).
		Int("headlines_count", len(headlines)).
		Msg("Démarrage source-noise")

	// ── Context avec gestion du shutdown propre (SIGTERM / SIGINT) ───────────
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// ── Connexion Kafka ──────────────────────────────────────────────────────
	writer, err := connectKafka(ctx, cfg.KafkaBrokers, cfg.KafkaTopic)
	if err != nil {
		log.Fatal().Err(err).Msg("Connexion Kafka impossible")
	}
	defer writer.Close()

	log.Info().Msg("Début de la génération de bruit...")

	// ── Boucle infinie de publication de bruit ────────────────────────────────
	// Le service tourne indéfiniment et ne s'arrête qu'à réception d'un signal
	messagesPublished := 0

	for {
		// Vérification du signal d'arrêt avant chaque publication
		select {
		case <-ctx.Done():
			log.Info().Msgf("Arrêt propre — %d messages de bruit publiés au total", messagesPublished)
			return
		default:
		}

		// Calcul de l'intervalle aléatoire avant la prochaine publication
		interval := randomInterval(cfg.IntervalMinMs, cfg.IntervalMaxMs)

		// Publication d'un message de bruit aléatoire
		source, headline, err := publishNoise(ctx, writer)
		if err != nil {
			log.Error().Err(err).Msg("Erreur lors de la publication du bruit")
			// Attente courte avant de réessayer en cas d'erreur transitoire
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}

		messagesPublished++

		// Log du message publié avec la source courte entre crochets
		log.Info().Msgf("📰 [%s] \"%s\"", source, headline)

		// Attente de l'intervalle aléatoire avant la prochaine publication
		// L'attente est interruptible par le signal d'arrêt
		select {
		case <-ctx.Done():
			log.Info().Msgf("Arrêt propre — %d messages de bruit publiés au total", messagesPublished)
			return
		case <-time.After(interval):
		}
	}
}
