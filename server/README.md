# Qaf API Server (سرور و رابط برنامه‌نویسی قاف)

High-performance, lightweight Go HTTP API serving the complete reference archive of Persian literature from `qaf.db` in read-only mode, with FTS5 sub-millisecond search, automatic Gzip compression, and full CORS support for the Capacitor client.

---

## Features

- **Blazing Fast**: Pure Go HTTP API serving SQLite with connection pooling, WAL mode, and memory-mapped I/O.
- **Read-Only & Thread-Safe**: Database opened in `mode=ro` with concurrent read connection pool.
- **FTS5 Persian Search**: Sub-5ms full-text search across 3,000,000+ verses with Persian character normalization (ی/ي, ک/ك, اعراب, نیم‌فاصله).
- **Zero Overhead**: Zero CGO dependencies (`modernc.org/sqlite`), cross-compiles easily to Linux amd64/arm64.
- **Production Ready**: Systemd unit file, Caddyfile with automatic free HTTPS, structured logging, and Gzip compression.

---

## Directory Structure

```
server/
├── Caddyfile                  # Production Caddy configuration (auto-HTTPS)
├── go.mod                     # Go module definitions
├── go.sum                     # Go module checksums
├── inspect_db.py              # Database diagnostics utility
├── main.go                    # Core HTTP API server & route handlers
├── optimize_db.go             # FTS5 search indexing & database optimizer
├── qaf.db                     # Uncompressed SQLite database (335 MB)
├── README.md                  # VPS build and deployment guide
└── systemd/
    └── qaf-api.service        # Systemd service unit configuration
```

---

## Production Deployment on VPS (Ubuntu / Debian)

### 1. Prerequisites

On your VPS (e.g. Ubuntu 22.04 or 24.04 LTS):

```bash
# Update packages
sudo apt update && sudo apt upgrade -y

# Install Go (1.22+ required)
sudo apt install -y golang-go git curl

# Install Caddy web server
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install -y caddy
```

### 2. Setup Project Directory

```bash
# Create app directory
sudo mkdir -p /opt/qaf/server
sudo chown -R $USER:$USER /opt/qaf

# Clone or copy repository files to /opt/qaf
cd /opt/qaf/server
```

### 3. Download, Decompress, and Optimize Database

Download the original database archive directly to your VPS from GitHub's CDN (takes ~2 seconds with VPS speeds):
```bash
curl -L -o qaf.db.gz "https://media.githubusercontent.com/media/thecurumo/qaf_app/ce8d861/capacitor-app/www/assets/databases/qaf.db.gz"
```

Decompress it:
```bash
gzip -d qaf.db.gz
```

Build the FTS5 search index (takes ~20 seconds to index 3M+ verses):
```bash
go run optimize_db.go
```
This builds the FTS5 full-text table for instant verse and poem searches and optimizes SQLite indexes.

### 4. Build the Go Binary

Compile an optimized, stripped binary:
```bash
go build -ldflags="-s -w" -o qaf-api main.go
```

Verify it runs:
```bash
./qaf-api
```
By default, the server listens on `http://127.0.0.1:8080`.

### 5. Install Systemd Service

```bash
# Copy systemd unit file
sudo cp systemd/qaf-api.service /etc/systemd/system/

# Ensure permissions
sudo chown -R www-data:www-data /opt/qaf

# Reload systemd, enable and start service
sudo systemctl daemon-reload
sudo systemctl enable qaf-api
sudo systemctl start qaf-api

# Check status and logs
sudo systemctl status qaf-api
sudo journalctl -u qaf-api -f
```

### 6. Configure Caddy (Automatic Free HTTPS)

Edit `/opt/qaf/server/Caddyfile` with your domain:
```bash
sudo nano /opt/qaf/server/Caddyfile
```
Replace `api.yourdomain.com` with your domain pointing to your VPS IP.

Copy Caddyfile and reload Caddy:
```bash
sudo cp Caddyfile /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Caddy automatically provisions free SSL/TLS certificates via Let's Encrypt / ZeroSSL and proxies requests to `http://127.0.0.1:8080`.

---

## API Endpoints Reference

### 1. Health Check
`GET /api/health`
- **Response**: `{"status": "ok"}`

### 2. Home Screen
`GET /api/home`
- **Response**:
  ```json
  {
    "featured_poets": [ { "id": 2, "name": "حافظ", ... } ],
    "featured_works": [ { "cat_id": 33, "title": "دیوان اشعار", "poet_name": "حافظ", ... } ]
  }
  ```

### 3. Filter Options (Cached Dropdowns)
`GET /api/filter-options`
- **Response**:
  ```json
  {
    "poets": [ { "id": 2, "name": "حافظ" }, ... ],
    "centuries": [ 3, 4, 5, 6, 7, 8, 9, 10, ... ],
    "cities": [ "بلخ", "شیراز", "نیشابور", ... ]
  }
  ```

### 4. Poets Directory
`GET /api/poets?century=&city=&sort=historical-asc&q=&offset=0&limit=30`
- **Query Parameters**:
  - `century`: e.g. `8` or `__other__` (contemporary/unknown)
  - `city`: city name substring
  - `sort`: `historical-asc` (default), `historical-desc`, `alphabetical`
  - `q`: search keyword
  - `offset`: pagination offset
  - `limit`: page size (default 30, max 100)
- **Response**: Array of poet objects.

### 5. Poet Details & Works
`GET /api/poets/:id`
- **Response**:
  ```json
  {
    "poet": { "id": 2, "name": "حافظ", "century_id": 8, "birthplace": "شیراز", ... },
    "works": [ { "id": 33, "text": "دیوان اشعار", "parent_id": 2 }, ... ]
  }
  ```

### 6. Works Directory
`GET /api/works?century=&poet=&sort=historical-asc&q=&offset=0&limit=30`
- **Query Parameters**:
  - `century`: century filter
  - `poet`: poet ID filter
  - `sort`: `historical-asc` (default), `historical-desc`, `alphabetical`
  - `q`: search keyword
- **Response**: Array of top-level works.

### 7. Work Hierarchical Navigation / Poems
`GET /api/works/:catId?q=&offset=0&limit=30`
- If the category has sub-categories: returns `children` array and empty `poems`.
- If the category is a leaf (contains poems): returns empty `children` and paginated `poems` array with first-couplet `preview`.
- **Response**:
  ```json
  {
    "work": { "id": 33, "text": "غزلیات", "poet_name": "حافظ", ... },
    "children": [ ... ],
    "poems": [ { "poem_id": 1, "title": "غزل ۱", "preview": "الا یا ایها الساقی...", ... } ]
  }
  ```

### 8. Work Poems Direct Pagination
`GET /api/works/:catId/poems?q=&offset=0&limit=30`
- **Response**: Array of poem cards with preview.

### 9. Poem Reader
`GET /api/poems/:id`
- Returns poem metadata, sibling poem IDs (for next/previous navigation), and verses grouped by `vorder`.
- **Response**:
  ```json
  {
    "poem": { "poem_id": 1, "title": "غزل شماره ۱", "cat_id": 33, "cat_title": "غزلیات", "poet_name": "حافظ", ... },
    "verses": [
      {
        "order": 1,
        "items": [
          { "poem_id": 1, "vorder": 1, "position": 0, "text": "الا یا ایها الساقی ادر کأسا و ناولها" },
          { "poem_id": 1, "vorder": 1, "position": 1, "text": "که عشق آسان نمود اول ولی افتاد مشکل‌ها" }
        ]
      }
    ],
    "poem_ids": [ 1, 2, 3, 4, ... ]
  }
  ```

### 10. Unified Search
`GET /api/search?q=عشق&scope=all&poet=&century=&limit=30`
- **Query Parameters**:
  - `q`: search query string
  - `scope`: `all`, `poets`, `works`, `poems`, `verses`
  - `poet`: filter by poet ID
  - `century`: filter by century
  - `limit`: results per category
- **Response**:
  ```json
  {
    "poets": [ ... ],
    "works": [ ... ],
    "poems": [ ... ],
    "verses": [ ... ]
  }
  ```

### 11. Batch Bookmarks Resolver
`POST /api/bookmarks`
- Resolves poet, work, poem, and verse bookmarks in a single fast query.
- **Request Body**:
  ```json
  {
    "poets": [2, 3],
    "works": [33],
    "poems": [1],
    "verses": ["1:1"]
  }
  ```
- **Response**:
  ```json
  {
    "poets": [ ... ],
    "works": [ ... ],
    "poems": [ ... ],
    "verses": [ ... ]
  }
  ```

### 12. Ceremonial Faal-e Hafez
`GET /api/faal` (or `GET /api/faal?id=:poemId`)
- Selects a random Ghazal from Hafez (`poet_id = 2`, `cat_id = 24`), or returns a specific Ghazal if `id` or `poem_id` is supplied.
- Generates or maps an authentic poetic interpretation (`تعبیر فال`) providing thoughtful, inspiring spiritual/life guidance.
- Sets `no-cache` headers to ensure instant new randomness upon repeated invocation.
- **Response**:
  ```json
  {
    "poem": {
      "poem_id": 2130,
      "title": "غزل شمارهٔ ۱",
      "cat_id": 24,
      "cat_title": "غزلیات",
      "poet_id": 2,
      "poet_name": "حافظ",
      "century_id": 8,
      "birthplace": "شیراز"
    },
    "verses": [
      {
        "order": 1,
        "items": [
          { "poem_id": 2130, "vorder": 1, "position": 0, "text": "اَلا یا اَیُّهَا السّاقی اَدِرْ کَأسَاً و ناوِلْها" },
          { "poem_id": 2130, "vorder": 2, "position": 1, "text": "که عشق آسان نمود اوّل ولی افتاد مشکل‌ها" }
        ]
      }
    ],
    "interpretation": "ای صاحب فال! کاری که در پیش گرفته‌ای در آغاز سهل و هموار می‌نماید...",
    "poem_ids": [ 2130, 2131, 2132, ... ]
  }
  ```

### 13. Daily Masterpiece Couplet (`بیت امروز`)
`GET /api/daily` (or `GET /api/daily?random=1`)
- Deterministically returns today's featured couplet on Iran Standard Time (`UTC+03:30`), or a random masterpiece couplet when `random=1`.

### 14. Mood / Vibe Discovery (`شعرِ حالِ تو`)
`GET /api/discover?mood=hope|love|calm|wisdom|joy|mystic&limit=4`
- Returns diverse classical couplets matching the requested emotional theme.

### 15. Interactive Mushaira Engine (`مشاعره هوشمند`)
`GET /api/mushaira?letter=د&user_verse=...&exclude=123,456&limit=6`
- Returns classical couplets starting with `letter` (or countering `user_verse`'s ending letter) along with `verified_in_archive` validation.

### 16. Classical Rhyme & Radif Finder (`قافیه‌یاب`)
`GET /api/rhyme?word=بهار&limit=20`
- Extracts rhyming words (`rhymes`) and classical example couplets (`couplets`) ranked by prosodic suffix match, automatically stripping shared trailing radifs.

### 17. Offline Poet Pack (`بسته مطالعه آفلاین`)
`GET /api/poets/:id/pack?limit=40`
- Batch-fetches up to 80 poems with grouped 2-hemistich couplets for 1-tap offline caching on client devices.

---

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Port for the HTTP server |
| `DB_PATH` | `./qaf.db` | Absolute or relative path to the SQLite `qaf.db` file |
| `ALLOWED_ORIGIN` | `*` | Allowed CORS origin header |
