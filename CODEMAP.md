# CODEMAP

Automatisch erstellte Übersicht über alle Ordner und Functions im Backend.
Wird manuell gepflegt — bei größeren Änderungen an Handlern/Packages
diese Datei aktualisieren.

## `cmd/server`

Läuft als eigener Prozess/Container — nur HTTP-API, kein Refresher mehr (siehe `cmd/worker`).

### `main.go`

| Function | Beschreibung |
|---|---|
| `envInt(key, def) int` | Liest einen int-Env-Var, Fallback auf `def` bei fehlend/ungültig/`<= 0`. |
| `main()` | Wire-up: DB-Verbindung, Migrationen, alle Handler/Routen/Middleware, Server-Start mit Graceful Shutdown bei SIGINT/SIGTERM. |

## `cmd/worker`

Eigener Prozess/Container für den Podcast-Feed-Refresher — bewusst von `cmd/server` getrennt, damit die
API horizontal skaliert werden kann, ohne dass jede Instanz eigenständig Feeds refresht und doppelte
Push-Notifications verschickt. Immer nur **1 Replica** dieses Prozesses laufen lassen. Setzt voraus, dass
das Schema bereits migriert ist (Migrationen laufen nur in `cmd/server`).

### `main.go`

| Function | Beschreibung |
|---|---|
| `main()` | DB-Verbindung, startet den Refresher-Loop (`podcast.NewRefresher(...).Run(...)`) in einer Goroutine, Graceful Shutdown bei SIGINT/SIGTERM. |

## `docs`

### `docs.go`

| Function | Beschreibung |
|---|---|
| `SpecHandler() http.HandlerFunc` | Liefert die eingebettete `openapi.yaml` roh aus. |
| `UIHandler() http.HandlerFunc` | Liefert eine Swagger-UI-HTML-Seite, die gegen `/openapi.yaml` läuft. |

## `internal/admin`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `Stats(w, r)` | Liefert Plattform-Kennzahlen (User-, Podcast-, Subscription-Anzahl, Verteilung nach Plan). |
| `ListUsers(w, r)` | Listet bis zu 500 User inkl. Subscription-Anzahl, neueste zuerst. |
| `GetUser(w, r)` | Liefert Details zu einem User anhand `id` (URL-Param). |
| `SetPlan(w, r)` | Setzt den Plan (`free`/`basic`/`pro`) eines Users. |
| `Suspend(w, r)` | Sperrt einen User (setzt `suspended_at`) und widerruft transaktional alle seine Refresh-Tokens. |
| `Unsuspend(w, r)` | Hebt die Sperre eines Users auf. |
| `DeleteUser(w, r)` | Löscht einen User endgültig. |

### `middleware.go`

| Function | Beschreibung |
|---|---|
| `Middleware(db) func(http.Handler) http.Handler` | Prüft `is_admin = true` in der DB für den authentifizierten User, sonst `403`. |

## `internal/auth`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db, m) *Handler` | Konstruktor, hält DB-Pool und Mailer. |
| `newAuthResponse(ctx, id, email) (authResponse, error)` | Erzeugt Access+Refresh-Token, speichert den Refresh-JTI in der DB, baut die Response zusammen. |
| `Register(w, r)` | Legt neuen User an (bcrypt-Hash, Cost 12), verschickt asynchron eine Verifizierungs-Mail, gibt Tokens zurück. |
| `Login(w, r)` | Prüft E-Mail/Passwort, blockt gesperrte Accounts, gibt neue Tokens zurück. |
| `Refresh(w, r)` | Validiert Refresh-Token gegen JWT + DB (JTI nicht widerrufen/abgelaufen), stellt neue Tokens aus. |
| `Logout(w, r)` | Widerruft den Refresh-Token (JTI). Antwortet immer mit `204`, auch bei ungültigem Token (keine Info-Leaks). |
| `ForgotPassword(w, r)` | Erstellt einen Password-Reset-Token, verschickt Reset-Mail. Antwortet immer `204`, egal ob E-Mail existiert. |
| `ResetPassword(w, r)` | Setzt neues Passwort per gültigem Reset-Token, widerruft danach alle Refresh-Tokens des Users. |
| `VerifyEmail(w, r)` | Markiert E-Mail als verifiziert anhand des Tokens aus der Query. |
| `sendVerificationEmail(ctx, userID, email)` | Erstellt Verifizierungs-Token und verschickt die Mail (wird als Goroutine aus `Register` aufgerufen). |

### `jwt.go`

| Function | Beschreibung |
|---|---|
| `GenerateAccessToken(userID) (string, error)` | Signiert Access-Token, TTL 15 Minuten. |
| `GenerateRefreshToken(userID) (token, jti, err)` | Erzeugt eine JTI und signiert Refresh-Token, TTL 30 Tage. |
| `signToken(userID, tokenType, jti, ttl) (string, error)` | Baut Claims und signiert das JWT (HS256, Secret aus `JWT_SECRET`). |
| `ValidateToken(tokenStr) (*Claims, error)` | Parst und validiert ein JWT, prüft Signaturmethode. |
| `newJTI() (string, error)` | Generiert eine zufällige UUID-ähnliche ID für Refresh-Tokens. |

### `middleware.go`

| Function | Beschreibung |
|---|---|
| `Middleware(next) http.Handler` | Liest `Authorization: Bearer`, validiert Access-Token, legt UserID in den Request-Context. |
| `UserIDFromCtx(r) string` | Liest die UserID aus dem Request-Context. |

## `internal/billing`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `stripeEnabled() bool` | Prüft, ob `STRIPE_SECRET_KEY` gesetzt ist. |
| `Status(w, r)` | Liefert Plan + Stripe-Customer-ID des eingeloggten Users. |
| `Checkout(w, r)` | Erstellt eine Stripe-Checkout-Session für `basic`/`pro`; `501`, falls Billing nicht konfiguriert. |
| `Portal(w, r)` | Erstellt eine Stripe-Billing-Portal-Session für den User (Selbstverwaltung des Abos). |
| `Webhook(w, r)` | Verarbeitet Stripe-Events (Signatur-Check, Idempotenz via `stripe_events`-Tabelle): Checkout abgeschlossen → Plan setzen, Subscription geändert → Plan anpassen, Subscription gelöscht → zurück auf `free`. |
| `priceToplan(priceID) string` | Mappt eine Stripe-Price-ID (aus Env) auf den internen Plan-Namen. |

### `middleware.go`

| Function | Beschreibung |
|---|---|
| `RequirePlan(db, plans...) func(http.Handler) http.Handler` | Prüft, ob der Plan des eingeloggten Users in der erlaubten Menge ist (z.B. `"basic","pro"` für Sync-Routen, `"pro"` für Pro-only); sonst `402 Payment Required`. Das eigentliche Paywall-Gate. |

## `internal/db`

### `db.go`

| Function | Beschreibung |
|---|---|
| `New(ctx) (*pgxpool.Pool, error)` | Baut den Postgres-Connection-Pool aus `DATABASE_URL` auf (inkl. optionaler `DB_MAX_CONNS`/`DB_MIN_CONNS`), pingt zur Verifikation. |

### `migrate.go`

| Function | Beschreibung |
|---|---|
| `RunMigrations(ctx, pool) error` | Legt `schema_migrations`-Tabelle an, wendet alle noch nicht angewendeten `.sql`-Dateien aus `migrations/` (sortiert) an und trackt sie. Serialisiert sich über einen Postgres-Advisory-Lock — sicher bei mehreren gleichzeitig startenden Instanzen (z.B. Fly-Rolling-Deploy). |

## `internal/episode`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `Search(w, r)` | Volltextsuche (ILIKE) über Episodentitel, nur innerhalb der Abos des Users. |
| `GetEpisode(w, r)` | Liefert eine einzelne Episode per ID. |
| `GetProgress(w, r)` | Liefert Hörfortschritt (Position, completed) für eine Episode; Zero-State, falls noch nichts gespeichert. |
| `UpsertProgress(w, r)` | Speichert/aktualisiert Hörfortschritt für eine Episode (Upsert). |

## `internal/favorite`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `List(w, r)` | Listet alle favorisierten Podcasts des Users, neueste zuerst. |
| `Add(w, r)` | Favorisiert einen Podcast. |
| `Remove(w, r)` | Entfernt eine Favorisierung. |

Sync-gated (Plan `basic`/`pro`) — siehe `billing.RequirePlan` in `cmd/server/main.go`.

## `internal/httperr`

### `httperr.go`

| Function | Beschreibung |
|---|---|
| `Write(w, status, msg)` | Schreibt eine konsistente JSON-Fehlerantwort `{"error": "..."}`. |

## `internal/mailer`

### `mailer.go`

| Function | Beschreibung |
|---|---|
| `New() *Mailer` | Baut den Mailer aus SMTP-Env-Vars (Host/Port/User/Pass/From). |
| `Send(to, subject, body) error` | Verschickt eine Plain-Text-Mail; loggt nur nach stdout, falls `SMTP_HOST` nicht gesetzt (Dev-Modus). |

## `internal/metrics`

### `metrics.go`

| Function | Beschreibung |
|---|---|
| `RegisterDBStats(pool)` | Registriert Prometheus-Gauges für idle/total DB-Pool-Connections. |
| `Handler() http.Handler` | Liefert den Prometheus-Scrape-Endpoint (`promhttp.Handler()`). |
| `Middleware(next) http.Handler` | Zählt Requests und misst Latenz pro chi-Routen-Pattern. |
| `(rw *responseWriter) WriteHeader(code)` | Fängt den Status-Code ab, damit die Middleware ihn für die Metrik kennt. |

## `internal/podcast`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `parseDurationSecs(s) *int` | Wandelt iTunes-Dauer-Strings (`H:MM:SS`, `MM:SS`, reine Sekunden) in Sekunden um. |
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `Fetch(w, r)` | Parst eine RSS-URL (gofeed) und upserted Podcast + alle Episoden in die DB. |
| `GetPodcast(w, r)` | Liefert Podcast-Details per ID. |
| `Search(w, r)` | Proxied die iTunes-Podcast-Such-API und mappt die Ergebnisse. |
| `Refresh(w, r)` | Fetcht die RSS-Feed-URL eines bestehenden Podcasts erneut (`UpsertFeed`). |
| `GetEpisodes(w, r)` | Liefert Episoden eines Podcasts, paginiert (`limit`/`offset`). |

### `refresher.go`

| Function | Beschreibung |
|---|---|
| `NewRefresher(db, notifier) *Refresher` | Konstruktor für den Background-Refresher. |
| `Run(ctx, interval)` | Refresht sofort einmal, danach in festem Intervall, bis `ctx` abgebrochen wird. |
| `refreshAll(ctx)` | Holt alle abonnierten Podcasts (distinct) und refresht sie nacheinander. |
| `refreshOne(ctx, podcastID, rssURL) error` | Refresht einen einzelnen Feed und stößt asynchron die Subscriber-Benachrichtigung an. |
| `notifySubscribers(ctx, podcastID)` | Prüft auf neue Episoden der letzten Stunde und schickt Push-Notifications an alle Abonnenten-Devices. |

### `rss.go`

| Function | Beschreibung |
|---|---|
| `UpsertFeed(ctx, db, rssURL) (podcastID, err)` | Fetcht eine RSS-URL (30s Timeout), upserted Podcast + Episoden, zentrale Funktion, die von `handler.go` und `refresher.go`/`subscription` genutzt wird. |

## `internal/push`

### `fcm.go`

| Function | Beschreibung |
|---|---|
| `NewNotifier() *Notifier` | Konstruktor, liest `FIREBASE_SERVER_KEY` aus Env. |
| `Send(ctx, token, title, body, data) error` | Schickt eine Push-Notification an ein einzelnes FCM-Device-Token; loggt nur, falls kein Server-Key gesetzt (Dev-Modus). |
| `SendToUser(ctx, tokens, title, body, data)` | Schickt dieselbe Notification an mehrere Device-Tokens eines Users. |
| `min(a, b) int` | Kleinerer der zwei Werte (Hilfsfunktion fürs sichere Token-Kürzen im Log). |

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `Register(w, r)` | Registriert/aktualisiert ein Device-Token (android/ios) für den User. |
| `Unregister(w, r)` | Entfernt ein Device-Token des Users. |

## `internal/queue`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `List(w, r)` | Liefert die Hörliste (Queue) des Users, sortiert nach Position. |
| `Add(w, r)` | Fügt eine Episode ans Ende der Queue an. |
| `Reorder(w, r)` | Setzt neue Reihenfolge anhand einer ID-Liste, transaktional. |
| `Remove(w, r)` | Entfernt einen Eintrag aus der Queue. |

## `internal/ratelimit`

### `middleware.go`

| Function | Beschreibung |
|---|---|
| `New(limit, window) func(http.Handler) http.Handler` | Erstellt einen In-Memory-Rate-Limiter (max. `limit` Requests pro `window` und IP), startet einen Cleanup-Goroutine. |
| `(l *limiter) middleware(next) http.Handler` | Zählt Requests pro IP im Sliding Window, blockt mit `429` bei Überschreitung. |
| `(l *limiter) cleanup()` | Räumt alle 5 Minuten abgelaufene Einträge aus der Request-Map, um Memory-Leaks zu vermeiden. |

## `internal/subscription`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `List(w, r)` | Listet alle Abos des Users, neueste zuerst. |
| `Subscribe(w, r)` | Abonniert einen Podcast. Kein Mengen-Limit — der Zugriff auf diese Route selbst ist die Paywall (`billing.RequirePlan`, Plan `basic`/`pro`). |
| `ImportOPML(w, r)` | Parst eine hochgeladene OPML-Datei (inkl. eine Ebene Verschachtelung), fetcht/upserted jeden Feed und abonniert ihn. |
| `ExportOPML(w, r)` | Exportiert alle Abos des Users als OPML-Datei zum Download. |
| `Unsubscribe(w, r)` | Entfernt ein Abo. |

## `internal/testutil`

### `testutil.go`

| Function | Beschreibung |
|---|---|
| `init()` | Setzt `JWT_SECRET` für Tests beim Package-Load. |
| `Pool(t) *pgxpool.Pool` | Verbindet zu `TEST_DATABASE_URL`, migriert, truncated Tabellen; Test wird geskippt, falls die Env-Var fehlt. |
| `clean(pool)` | Truncated `users` und `podcasts` (jeweils `CASCADE`) — deckt damit alle App-Tabellen ab, da nichts außer diesen beiden Wurzeln referenziert wird. |
| `CreateUser(t, pool, email, password) string` | Legt einen User direkt in der DB an (schneller bcrypt-Cost für Tests), gibt die UUID zurück. |
| `CreateAdminUser(t, pool, email, password) string` | Wie `CreateUser`, setzt zusätzlich `is_admin = TRUE`. |
| `CreatePodcast(t, pool) string` | Legt einen minimalen Test-Podcast an. |
| `CreateEpisode(t, pool, podcastID) string` | Legt eine minimale Test-Episode an. |
| `AccessToken(t, userID) string` | Generiert ein gültiges Access-Token für Tests. |
| `WithUser(r, userID) *http.Request` | Injected eine UserID in den Request-Context, ohne die JWT-Middleware zu durchlaufen. |

## `internal/user`

### `handler.go`

| Function | Beschreibung |
|---|---|
| `NewHandler(db) *Handler` | Konstruktor, hält den DB-Pool. |
| `Feed(w, r)` | Liefert die neuesten 100 Episoden aus allen Abos inkl. Hörfortschritt. |
| `ContinueListening(w, r)` | Liefert angefangene, nicht abgeschlossene Episoden, zuletzt gehört zuerst. |
| `History(w, r)` | Liefert abgeschlossene Episoden, zuletzt abgeschlossen zuerst. |
| `Stats(w, r)` | Aggregiert Hör-Statistiken (abgeschlossen/in Arbeit/Gesamtzeit/Abo-Anzahl) in einer Query. |
| `ChangePassword(w, r)` | Ändert das Passwort nach Prüfung des aktuellen; widerruft danach alle Refresh-Tokens. |
| `Delete(w, r)` | Löscht den eigenen Account. |
| `Me(w, r)` | Liefert Basis-Profildaten des eingeloggten Users. |

## `migrations`

Enthält nur nummerierte, reine SQL-Dateien (`00N_name.sql`), keine Go-Functions.
Werden beim Serverstart in Reihenfolge angewendet, siehe `internal/db/migrate.go`.
