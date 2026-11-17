package main

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// Global database handle and cached filter options
var (
	db            *sql.DB
	filterCache   *FilterOptions
	filterCacheMu sync.RWMutex
	gzipPool      = sync.Pool{
		New: func() interface{} {
			w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
			return w
		},
	}
)

// Data Models
type HealthResponse struct {
	Status string `json:"status"`
}

type Poet struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	CatID       *int64  `json:"cat_id,omitempty"`
	Description *string `json:"description,omitempty"`
	CenturyID   *int64  `json:"century_id,omitempty"`
	BirthYear   *int64  `json:"birth_year,omitempty"`
	DeathYear   *int64  `json:"death_year,omitempty"`
	Birthplace  *string `json:"birthplace,omitempty"`
	Deathplace  *string `json:"deathplace,omitempty"`
}

type WorkItem struct {
	CatID      int64   `json:"cat_id"`
	Title      string  `json:"title"`
	PoetName   string  `json:"poet_name"`
	BirthYear  *int64  `json:"birth_year,omitempty"`
	Birthplace *string `json:"birthplace,omitempty"`
	CenturyID  *int64  `json:"century_id,omitempty"`
	ParentID   *int64  `json:"parent_id,omitempty"`
}

type HomeResponse struct {
	FeaturedPoets []Poet     `json:"featured_poets"`
	FeaturedWorks []WorkItem `json:"featured_works"`
}

type FilterOptions struct {
	Poets     []PoetOption `json:"poets"`
	Centuries []int64      `json:"centuries"`
	Cities    []string     `json:"cities"`
}

type PoetOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type CatSimple struct {
	ID       int64  `json:"id"`
	Text     string `json:"text"`
	ParentID int64  `json:"parent_id"`
}

type PoetDetailResponse struct {
	Poet  Poet        `json:"poet"`
	Works []CatSimple `json:"works"`
}

type CatDetail struct {
	ID         int64   `json:"id"`
	Text       string  `json:"text"`
	ParentID   int64   `json:"parent_id"`
	URL        *string `json:"url,omitempty"`
	PoetID     int64   `json:"poet_id"`
	PoetName   string  `json:"poet_name"`
	CenturyID  *int64  `json:"century_id,omitempty"`
	Birthplace *string `json:"birthplace,omitempty"`
	BirthYear  *int64  `json:"birth_year,omitempty"`
}

type FlatPoem struct {
	PoemID   int64   `json:"poem_id"`
	Title    string  `json:"title"`
	CatTitle string  `json:"cat_title"`
	PoetName string  `json:"poet_name"`
	Preview  *string `json:"preview,omitempty"`
}

type WorkDetailResponse struct {
	Work     *CatDetail  `json:"work"`
	Children []CatSimple `json:"children"`
	Poems    []FlatPoem  `json:"poems"`
}

type PoemDetail struct {
	PoemID     int64   `json:"poem_id"`
	Title      string  `json:"title"`
	CatID      int64   `json:"cat_id"`
	CatTitle   string  `json:"cat_title"`
	PoetID     int64   `json:"poet_id"`
	PoetName   string  `json:"poet_name"`
	CenturyID  *int64  `json:"century_id,omitempty"`
	Birthplace *string `json:"birthplace,omitempty"`
}

type VerseItem struct {
	PoemID   int64  `json:"poem_id"`
	VOrder   int64  `json:"vorder"`
	Position int64  `json:"position"`
	Text     string `json:"text"`
}

type VerseGroup struct {
	Order int64       `json:"order"`
	Items []VerseItem `json:"items"`
}

type PoemDetailResponse struct {
	Poem    *PoemDetail  `json:"poem"`
	Verses  []VerseGroup `json:"verses"`
	PoemIDs []int64      `json:"poem_ids"`
}

type FaalResponse struct {
	Poem           *PoemDetail  `json:"poem"`
	Verses         []VerseGroup `json:"verses"`
	Interpretation string       `json:"interpretation"`
	PoemIDs        []int64      `json:"poem_ids"`
}

type SearchVerseResult struct {
	PoemID    int64  `json:"poem_id"`
	VOrder    int64  `json:"vorder"`
	Position  int64  `json:"position"`
	Text      string `json:"text"`
	Title     string `json:"title"`
	PoetName  string `json:"poet_name"`
	PoetID    int64  `json:"poet_id"`
	CenturyID *int64 `json:"century_id,omitempty"`
	CatTitle  string `json:"cat_title"`
}

type SearchPoemResult struct {
	PoemID    int64  `json:"poem_id"`
	Title     string `json:"title"`
	PoetName  string `json:"poet_name"`
	CatTitle  string `json:"cat_title"`
	CenturyID *int64 `json:"century_id,omitempty"`
}

type SearchResponse struct {
	Poets  []Poet              `json:"poets"`
	Works  []WorkItem          `json:"works"`
	Poems  []SearchPoemResult  `json:"poems"`
	Verses []SearchVerseResult `json:"verses"`
}

type BookmarkRequest struct {
	Poets  []int64  `json:"poets"`
	Works  []int64  `json:"works"`
	Poems  []int64  `json:"poems"`
	Verses []string `json:"verses"` // Format: "poem_id:vorder" or just poem_id
}

type BookmarkResponse struct {
	Poets  []Poet              `json:"poets"`
	Works  []WorkItem          `json:"works"`
	Poems  []SearchPoemResult  `json:"poems"`
	Verses []SearchVerseResult `json:"verses"`
}

// Helpers
func normalizePersian(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u064A', '\u0649', '\u0626': // Arabic Yeh, Alef Maksura, Yeh with hamza -> Persian Yeh
			b.WriteRune('\u06CC')
		case '\u0643': // Arabic Kaf -> Persian Kaf
			b.WriteRune('\u06A9')
		case '\u0623', '\u0625', '\u0671': // Alef with hamza/wasla -> Alef (keep \u0622 'آ' intact for FTS5!)
			b.WriteRune('\u0627')
		case '\u0640': // Tatweel
			continue
		case '\u064B', '\u064C', '\u064D', '\u064E', '\u064F', '\u0650', '\u0651', '\u0652', '\u0653', '\u0654', '\u0655', '\u0670': // Diacritics & hamza marks
			continue
		case '\u200C', '\u200D': // ZWNJ / ZWJ -> space
			b.WriteRune(' ')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Fold Alef-Madda ('آ') -> 'ا' for SQL LIKE comparisons alongside sqlNormCol
func foldAlefLike(s string) string {
	return strings.ReplaceAll(s, "\u0622", "\u0627")
}

// SQL expression normalizing 'آ', ZWNJ (8204), and hamza-above (1620) on small B-tree columns
func sqlNormCol(col string) string {
	return fmt.Sprintf("REPLACE(REPLACE(REPLACE(COALESCE(%s,''), 'آ', 'ا'), CHAR(8204), ' '), CHAR(1620), '')", col)
}

// Build FTS5 MATCH expression expanding leading ا/آ so both 'آتش' and 'اتش' match 100% of verses
func buildFTSMatchExpr(text string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	var parts []string
	for _, w := range words {
		w = strings.NewReplacer(`"`, "", `*`, "", `(`, "", `)`, "", `:`, "", `^`, "").Replace(w)
		if w == "" {
			continue
		}
		if strings.HasPrefix(w, "آ") {
			rest := strings.TrimPrefix(w, "آ")
			parts = append(parts, fmt.Sprintf(`("%s"* OR "ا%s"*)`, w, rest))
		} else if strings.HasPrefix(w, "ا") {
			rest := strings.TrimPrefix(w, "ا")
			parts = append(parts, fmt.Sprintf(`("%s"* OR "آ%s"*)`, w, rest))
		} else {
			parts = append(parts, fmt.Sprintf(`"%s"*`, w))
		}
	}
	return strings.Join(parts, " AND ")
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Error encoding JSON: %v", err)
	}
}

// Gzip Compression Wrapper
type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.Header().Del("Content-Length")
		w.Header().Set("Content-Encoding", "gzip")
		w.ResponseWriter.WriteHeader(status)
		w.wroteHeader = true
	}
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.Writer.Write(b)
}

// Middleware
func corsAndGzipMiddleware(next http.Handler) http.Handler {
	allowedOrigin := os.Getenv("ALLOWED_ORIGIN")
	if allowedOrigin == "" {
		allowedOrigin = "*"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS headers
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Origin, X-Requested-With")
		w.Header().Set("Access-Control-Max-Age", "86400")
		w.Header().Add("Vary", "Accept-Encoding")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Cache control for static poetry queries
		if r.Method == http.MethodGet {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}

		// Gzip compression if requested
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			gz := gzipPool.Get().(*gzip.Writer)
			defer gzipPool.Put(gz)
			gz.Reset(w)
			defer gz.Close()

			gzw := &gzipResponseWriter{Writer: gz, ResponseWriter: w}
			next.ServeHTTP(gzw, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Handlers

// 1. Health
func handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := db.Ping(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok"})
}

// 2. Home
var (
	featuredPoetIDs = []int64{2, 3, 4, 5, 6, 7, 9, 12}
	featuredWorkIDs = []int64{33, 98, 103, 119, 123, 1665, 156, 255}
)

func handleHome(w http.ResponseWriter, r *http.Request) {
	// Query featured poets
	poetPlaceholders := strings.TrimRight(strings.Repeat("?,", len(featuredPoetIDs)), ",")
	poetArgs := make([]interface{}, len(featuredPoetIDs))
	for i, id := range featuredPoetIDs {
		poetArgs[i] = id
	}

	poetQuery := fmt.Sprintf(`SELECT id, name, cat_id, description, century_id, birth_year, death_year, birthplace, deathplace 
		FROM poet WHERE id IN (%s)`, poetPlaceholders)
	rows, err := db.Query(poetQuery, poetArgs...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	poetsMap := make(map[int64]Poet)
	for rows.Next() {
		var p Poet
		if err := rows.Scan(&p.ID, &p.Name, &p.CatID, &p.Description, &p.CenturyID, &p.BirthYear, &p.DeathYear, &p.Birthplace, &p.Deathplace); err != nil {
			continue
		}
		poetsMap[p.ID] = p
	}

	poets := make([]Poet, 0, len(featuredPoetIDs))
	for _, id := range featuredPoetIDs {
		if p, ok := poetsMap[id]; ok {
			poets = append(poets, p)
		}
	}

	// Query featured works
	workPlaceholders := strings.TrimRight(strings.Repeat("?,", len(featuredWorkIDs)), ",")
	workArgs := make([]interface{}, len(featuredWorkIDs))
	for i, id := range featuredWorkIDs {
		workArgs[i] = id
	}

	workQuery := fmt.Sprintf(`SELECT c.id AS cat_id, c.text AS title, p.name AS poet_name, p.birth_year, p.birthplace, p.century_id, c.parent_id
		FROM cat c JOIN poet p ON p.id=c.poet_id
		WHERE c.id IN (%s)`, workPlaceholders)
	wrows, err := db.Query(workQuery, workArgs...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer wrows.Close()

	worksMap := make(map[int64]WorkItem)
	for wrows.Next() {
		var item WorkItem
		if err := wrows.Scan(&item.CatID, &item.Title, &item.PoetName, &item.BirthYear, &item.Birthplace, &item.CenturyID, &item.ParentID); err != nil {
			continue
		}
		worksMap[item.CatID] = item
	}

	works := make([]WorkItem, 0, len(featuredWorkIDs))
	for _, id := range featuredWorkIDs {
		if item, ok := worksMap[id]; ok {
			works = append(works, item)
		}
	}

	writeJSON(w, http.StatusOK, HomeResponse{
		FeaturedPoets: poets,
		FeaturedWorks: works,
	})
}

// 3. Filter Options
func loadFilterOptions() (*FilterOptions, error) {
	filterCacheMu.RLock()
	if filterCache != nil {
		defer filterCacheMu.RUnlock()
		return filterCache, nil
	}
	filterCacheMu.RUnlock()

	filterCacheMu.Lock()
	defer filterCacheMu.Unlock()
	if filterCache != nil {
		return filterCache, nil
	}

	// Poets
	prows, err := db.Query(`SELECT id, name FROM poet ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	var poets []PoetOption
	for prows.Next() {
		var po PoetOption
		if err := prows.Scan(&po.ID, &po.Name); err == nil {
			poets = append(poets, po)
		}
	}

	// Centuries
	crows, err := db.Query(`SELECT DISTINCT century_id FROM poet WHERE century_id IS NOT NULL ORDER BY CAST(century_id AS INTEGER) ASC`)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	var centuries []int64
	for crows.Next() {
		var c int64
		if err := crows.Scan(&c); err == nil {
			centuries = append(centuries, c)
		}
	}

	// Cities
	ctrows, err := db.Query(`SELECT DISTINCT birthplace FROM poet WHERE birthplace IS NOT NULL AND TRIM(birthplace)<>'' ORDER BY birthplace`)
	if err != nil {
		return nil, err
	}
	defer ctrows.Close()
	var cities []string
	for ctrows.Next() {
		var city string
		if err := ctrows.Scan(&city); err == nil {
			cities = append(cities, city)
		}
	}

	filterCache = &FilterOptions{
		Poets:     poets,
		Centuries: centuries,
		Cities:    cities,
	}
	return filterCache, nil
}

func handleFilterOptions(w http.ResponseWriter, r *http.Request) {
	opts, err := loadFilterOptions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

// 4. Poets Directory
func handlePoets(w http.ResponseWriter, r *http.Request) {
	q := normalizePersian(r.URL.Query().Get("q"))
	century := r.URL.Query().Get("century")
	city := normalizePersian(r.URL.Query().Get("city"))
	sort := r.URL.Query().Get("sort")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}

	var where []string
	var args []interface{}

	if q != "" {
		likeQ := "%" + foldAlefLike(q) + "%"
		where = append(where, fmt.Sprintf("(%s LIKE ? OR %s LIKE ?)", sqlNormCol("name"), sqlNormCol("birthplace")))
		args = append(args, likeQ, likeQ)
	}
	if century != "" {
		if century == "__other__" {
			where = append(where, "century_id IS NULL")
		} else {
			where = append(where, "CAST(century_id AS TEXT) = ?")
			args = append(args, century)
		}
	}
	if city != "" {
		likeCity := "%" + foldAlefLike(city) + "%"
		where = append(where, fmt.Sprintf("(%s LIKE ? OR %s LIKE ?)", sqlNormCol("birthplace"), sqlNormCol("deathplace")))
		args = append(args, likeCity, likeCity)
	}

	order := "COALESCE(birth_year, 999999) ASC, name"
	if sort == "historical-desc" {
		order = "COALESCE(birth_year, -1) DESC, name"
	} else if sort == "alphabetical" {
		order = "name COLLATE NOCASE ASC"
	}

	query := "SELECT id, name, cat_id, description, century_id, birth_year, death_year, birthplace, deathplace FROM poet"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY %s LIMIT %d OFFSET %d", order, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	poets := make([]Poet, 0)
	for rows.Next() {
		var p Poet
		if err := rows.Scan(&p.ID, &p.Name, &p.CatID, &p.Description, &p.CenturyID, &p.BirthYear, &p.DeathYear, &p.Birthplace, &p.Deathplace); err == nil {
			poets = append(poets, p)
		}
	}
	writeJSON(w, http.StatusOK, poets)
}

// 5. Poet Details + Works
func handlePoetDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid poet ID", http.StatusBadRequest)
		return
	}

	var p Poet
	err = db.QueryRow(`SELECT id, name, cat_id, description, century_id, birth_year, death_year, birthplace, deathplace 
		FROM poet WHERE id = ? LIMIT 1`, id).
		Scan(&p.ID, &p.Name, &p.CatID, &p.Description, &p.CenturyID, &p.BirthYear, &p.DeathYear, &p.Birthplace, &p.Deathplace)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Poet not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Top level categories for this poet
	crows, err := db.Query(`
		SELECT c.id, c.text, c.parent_id
		FROM cat c
		WHERE c.parent_id = (SELECT r.id FROM cat r WHERE r.poet_id = ? AND r.parent_id = 0 LIMIT 1)
		ORDER BY c.id`, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer crows.Close()

	works := make([]CatSimple, 0)
	for crows.Next() {
		var c CatSimple
		if err := crows.Scan(&c.ID, &c.Text, &c.ParentID); err == nil {
			works = append(works, c)
		}
	}

	writeJSON(w, http.StatusOK, PoetDetailResponse{
		Poet:  p,
		Works: works,
	})
}

// 6. Works Directory
func handleWorks(w http.ResponseWriter, r *http.Request) {
	q := normalizePersian(r.URL.Query().Get("q"))
	century := r.URL.Query().Get("century")
	poet := r.URL.Query().Get("poet")
	sort := r.URL.Query().Get("sort")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}

	var where []string
	var args []interface{}

	if q != "" {
		likeQ := "%" + foldAlefLike(q) + "%"
		where = append(where, fmt.Sprintf("(%s LIKE ? OR %s LIKE ?)", sqlNormCol("c.text"), sqlNormCol("p.name")))
		args = append(args, likeQ, likeQ)
	}
	if poet != "" {
		where = append(where, "p.id = ?")
		args = append(args, poet)
	}
	if century != "" {
		if century == "__other__" {
			where = append(where, "p.century_id IS NULL")
		} else {
			where = append(where, "CAST(p.century_id AS TEXT) = ?")
			args = append(args, century)
		}
	}

	order := "COALESCE(p.birth_year, 999999) ASC, c.id"
	if sort == "historical-desc" {
		order = "COALESCE(p.birth_year, -1) DESC, c.id"
	} else if sort == "alphabetical" {
		order = "c.text COLLATE NOCASE ASC"
	}

	baseQuery := `SELECT c.id AS cat_id, c.text AS title, p.name AS poet_name, p.birth_year, p.birthplace, p.century_id, c.parent_id
		FROM cat c JOIN poet p ON p.id=c.poet_id JOIN cat root ON root.id=c.parent_id AND root.parent_id=0`
	if len(where) > 0 {
		baseQuery += " WHERE " + strings.Join(where, " AND ")
	}
	baseQuery += fmt.Sprintf(" ORDER BY %s LIMIT %d OFFSET %d", order, limit, offset)

	rows, err := db.Query(baseQuery, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	works := make([]WorkItem, 0)
	for rows.Next() {
		var item WorkItem
		if err := rows.Scan(&item.CatID, &item.Title, &item.PoetName, &item.BirthYear, &item.Birthplace, &item.CenturyID, &item.ParentID); err == nil {
			works = append(works, item)
		}
	}
	writeJSON(w, http.StatusOK, works)
}

// 7. Work Hierarchical Navigation (children or leaf poems)
func handleWorkDetail(w http.ResponseWriter, r *http.Request) {
	catIDStr := r.PathValue("catId")
	catID, err := strconv.ParseInt(catIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid catId", http.StatusBadRequest)
		return
	}

	var work CatDetail
	err = db.QueryRow(`
		SELECT c.id, c.text, c.parent_id, c.url, p.id AS poet_id, p.name AS poet_name, p.century_id, p.birthplace, p.birth_year 
		FROM cat c JOIN poet p ON p.id=c.poet_id 
		WHERE c.id = ? LIMIT 1`, catID).
		Scan(&work.ID, &work.Text, &work.ParentID, &work.URL, &work.PoetID, &work.PoetName, &work.CenturyID, &work.Birthplace, &work.BirthYear)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Work not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Check if this category has subcategories
	crows, err := db.Query(`SELECT id, text, parent_id FROM cat WHERE parent_id = ? ORDER BY id`, catID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer crows.Close()

	children := make([]CatSimple, 0)
	for crows.Next() {
		var c CatSimple
		if err := crows.Scan(&c.ID, &c.Text, &c.ParentID); err == nil {
			children = append(children, c)
		}
	}

	poems := make([]FlatPoem, 0)
	// If no children, it's a leaf node -> load first page of flat poems
	if len(children) == 0 {
		poems = queryLeafWorkPoems(catID, r)
	}

	writeJSON(w, http.StatusOK, WorkDetailResponse{
		Work:     &work,
		Children: children,
		Poems:    poems,
	})
}

func queryLeafWorkPoems(catID int64, r *http.Request) []FlatPoem {
	q := normalizePersian(r.URL.Query().Get("q"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 60
	}
	if offset < 0 {
		offset = 0
	}

	var where []string
	var args []interface{}
	where = append(where, "po.cat_id = ?")
	args = append(args, catID)

	if q != "" {
		likeQ := "%" + foldAlefLike(q) + "%"
		ftsExpr := buildFTSMatchExpr(q)
		if ftsExpr != "" {
			where = append(where, fmt.Sprintf("(%s LIKE ? OR po.id IN (SELECT v.poem_id FROM verse_fts f JOIN verse v ON v.rowid = f.rowid WHERE verse_fts MATCH ?))", sqlNormCol("po.title")))
			args = append(args, likeQ, ftsExpr)
		} else {
			where = append(where, fmt.Sprintf("%s LIKE ?", sqlNormCol("po.title")))
			args = append(args, likeQ)
		}
	}

	pquery := fmt.Sprintf(`SELECT po.id AS poem_id, po.title, c.text AS cat_title, p.name AS poet_name,
		(SELECT GROUP_CONCAT(text, ' / ') FROM (SELECT v.text FROM verse v WHERE v.poem_id=po.id ORDER BY v.vorder ASC LIMIT 2)) AS preview
		FROM poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id
		WHERE %s ORDER BY po.id LIMIT %d OFFSET %d`, strings.Join(where, " AND "), limit, offset)

	poems := make([]FlatPoem, 0)
	prows, err := db.Query(pquery, args...)
	if err != nil && q != "" {
		// Fallback if verse_fts is not present
		fallbackQuery := fmt.Sprintf(`SELECT po.id AS poem_id, po.title, c.text AS cat_title, p.name AS poet_name,
			(SELECT GROUP_CONCAT(text, ' / ') FROM (SELECT v.text FROM verse v WHERE v.poem_id=po.id ORDER BY v.vorder ASC LIMIT 2)) AS preview
			FROM poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id
			WHERE po.cat_id = ? AND %s LIKE ? ORDER BY po.id LIMIT %d OFFSET %d`, sqlNormCol("po.title"), limit, offset)
		prows, err = db.Query(fallbackQuery, catID, "%"+foldAlefLike(q)+"%")
	}
	if err == nil {
		defer prows.Close()
		for prows.Next() {
			var fp FlatPoem
			if err := prows.Scan(&fp.PoemID, &fp.Title, &fp.CatTitle, &fp.PoetName, &fp.Preview); err == nil {
				poems = append(poems, fp)
			}
		}
	}
	return poems
}

// 8. Work Poems Direct Pagination
func handleWorkPoems(w http.ResponseWriter, r *http.Request) {
	catIDStr := r.PathValue("catId")
	catID, err := strconv.ParseInt(catIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid catId", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, queryLeafWorkPoems(catID, r))
}

// Pair adjacent hemistichs (position 0+1 or centered 2+3) into real Persian couplets (ابیات)
func groupVerseItems(rawItems []VerseItem) []VerseGroup {
	verses := make([]VerseGroup, 0, (len(rawItems)+1)/2)
	for _, v := range rawItems {
		if len(verses) > 0 {
			last := &verses[len(verses)-1]
			if len(last.Items) == 1 {
				prevPos := last.Items[0].Position
				if (prevPos == 0 && v.Position == 1) || (prevPos == 2 && v.Position == 3) {
					last.Items = append(last.Items, v)
					continue
				}
			}
		}
		verses = append(verses, VerseGroup{
			Order: v.VOrder,
			Items: []VerseItem{v},
		})
	}
	return verses
}

// Helper to fetch poem metadata, sibling poem IDs, and grouped verses
func fetchPoemWithVerses(poemID int64) (*PoemDetail, []VerseGroup, []int64, error) {
	var poem PoemDetail
	err := db.QueryRow(`
		SELECT po.id AS poem_id, po.title, po.cat_id, c.text AS cat_title, p.id AS poet_id, p.name AS poet_name, p.century_id, p.birthplace
		FROM poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id 
		WHERE po.id = ? LIMIT 1`, poemID).
		Scan(&poem.PoemID, &poem.Title, &poem.CatID, &poem.CatTitle, &poem.PoetID, &poem.PoetName, &poem.CenturyID, &poem.Birthplace)
	if err != nil {
		return nil, nil, nil, err
	}

	// Sibling poem IDs for prev/next navigation
	srows, err := db.Query(`SELECT id FROM poem WHERE cat_id = ? ORDER BY id`, poem.CatID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer srows.Close()

	var poemIDs []int64
	for srows.Next() {
		var id int64
		if err := srows.Scan(&id); err == nil {
			poemIDs = append(poemIDs, id)
		}
	}

	// Verses
	vrows, err := db.Query(`SELECT poem_id, vorder, position, text FROM verse WHERE poem_id = ? ORDER BY vorder, position`, poemID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer vrows.Close()

	var rawItems []VerseItem
	for vrows.Next() {
		var v VerseItem
		if err := vrows.Scan(&v.PoemID, &v.VOrder, &v.Position, &v.Text); err == nil {
			rawItems = append(rawItems, v)
		}
	}

	verses := groupVerseItems(rawItems)
	return &poem, verses, poemIDs, nil
}

// 9. Poem Reader
func handlePoemDetail(w http.ResponseWriter, r *http.Request) {
	poemIDStr := r.PathValue("id")
	poemID, err := strconv.ParseInt(poemIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid poem ID", http.StatusBadRequest)
		return
	}

	poem, verses, poemIDs, err := fetchPoemWithVerses(poemID)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Poem not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	writeJSON(w, http.StatusOK, PoemDetailResponse{
		Poem:    poem,
		Verses:  verses,
		PoemIDs: poemIDs,
	})
}

// Hafez Ghazal Cache
var (
	hafezGhazalIDs   []int64
	hafezGhazalsMu   sync.RWMutex
	hafezGhazalsOnce sync.Once
)

func getHafezGhazalIDs() ([]int64, error) {
	hafezGhazalsMu.RLock()
	if len(hafezGhazalIDs) > 0 {
		defer hafezGhazalsMu.RUnlock()
		return hafezGhazalIDs, nil
	}
	hafezGhazalsMu.RUnlock()

	hafezGhazalsMu.Lock()
	defer hafezGhazalsMu.Unlock()
	if len(hafezGhazalIDs) > 0 {
		return hafezGhazalIDs, nil
	}

	rows, err := db.Query(`
		SELECT po.id 
		FROM poem po 
		JOIN cat c ON c.id = po.cat_id 
		WHERE c.poet_id = 2 AND (c.id = 24 OR c.text LIKE '%غزل%')
		ORDER BY po.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	hafezGhazalIDs = ids
	return hafezGhazalIDs, nil
}

var curatedFaalInterpretations = map[int64]string{
	2130: "ای صاحب فال! کاری که در پیش گرفته‌ای در آغاز سهل و هموار می‌نماید، اما موانع و دشواری‌هایی در مسیر خواهد بود که نیاز به اراده و استقامت دارد. نگران مباش؛ چرا که وصال مقصود و رسیدن به آرزوها همواره با آزمون‌های دل همراه است. پایمردی پیشه کن، از ملامت بدخواهان مهراس و با خلوص نیت و توکل به ذات حق گام بردار تا به سلامت از این گرداب گذر کنی.",
	2131: "ای صاحب فال! میان اندیشه و عمل تو فاصله‌ای پدید آمده و بر سر دوراهی مانده‌ای. از سرزنش دیگران و قضاوت‌های ظاهربینانه دلگیر مشو؛ حقیقت حال تو را تنها پروردگار می‌داند. در اصلاح امور خود بکوش، دل از تعلقات پوچ برکن و با صدق و صفای درون راهت را پی بگیر که آرامش واقعی در صدق نیت است.",
	2132: "ای صاحب فال! مژده باد که اقبال و سعادت به تو روی آورده است. برای رسیدن به اهداف بزرگت باید اهل بخشش، گذشت و فراخ‌دلی باشی. پند خیرخواهان و بزرگان با تجربه را با جان و دل بشنو، از غرور بیجا بپرهیز و بدان که اگر قدر لحظات خوش زندگی را بدانی، روزگار کام تو را شیرین خواهد کرد.",
	2133: "ای صاحب فال! پیامی خوش و نویدی روح‌بخش از سوی عزیزی در راه است. در دوری و انتظار صبور باش و تندی روزگار را به دل مگیر. اگر می‌خواهی بر دل‌ها حکومت کنی، تواضع و مهربانی را سرلوحه کارت قرار بده؛ چرا که شکوه و بزرگی با فروتنی کمال می‌یابد.",
	2134: "ای صاحب فال! رازی در سینه داری که نگه‌داشتن آن برایت دشوار شده و اضطراب بر جانت نشسته است. در تصمیم‌گیری‌های حساس از مشورت با اهل دل و دلسوزان دریغ مکن. در برابر طوفان حوادث شکیبا باش، توکل خود را از دست مده و کشتی وجودت را به ناخدای حقیقت بسپار تا به ساحل امن برسی.",
	2135: "ای صاحب فال! خواسته و تقاضایی از بزرگی یا سرپرستی داری. در بیان خواسته‌ات ادب، حرمت و دعا را پیشه ساز و شکرگزار جایگاه کنونی خود باش. با نیکی و فروتنی دل‌ها را به سوی خود متمایل کن تا درهای گشایش به رویت باز شود.",
	2136: "ای صاحب فال! کدورت‌ها و زنگار دل را پاک کن و از کبر و تعصب دوری گزین. جام وجودت را زلال دار تا حقیقت و زیبایی‌های عالم را بدون واسطه درک کنی. به ظاهر امور بسنده مکن و در پی صفای باطن باش.",
	2137: "ای صاحب فال! فرصت‌های زندگی چون ابر بهار در گذرند؛ پس امروز را دریاب و اندوه فرداهای نیامده را مخور. رخوت و سستی را کنار بگذار و با همتی تازه و روحی سرشار از نشاط به استقبال کارها برو. نیت پاک تو سرانجام به بار خواهد نشست.",
	2138: "ای صاحب فال! دوره‌ای تازه و بهاری در زندگیت در حال شکوفایی است. فرصت جوانی، انرژی و انگیزه خود را غنیمت شمار و پیش از آنکه دیر شود، پایه‌های موفقیت خود را مستحکم ساز. با اهل صفا همنشین شو و از بخل و بدخواهی دوری کن.",
	2139: "ای صاحب فال! در پی حقیقت باش و از ریاکاری و تظاهر بیزار باش. چه بسا کسانی که ادعای زهد و تقوا دارند اما دلشان خالی از صفاست. در مسیر معرفت و خدمت به خلق، راه راستی و خلوص را برگزین تا به آرامش روحی پایدار دست یابی.",
	2140: "ای صاحب فال! بشارت باد که روزگار به کام تو گشته و درهای بسته یکی پس از دیگری گشوده خواهد شد. نام نیک و آوازه بلند پاداش راستی و پاک‌دستی توست. در این ایام خجستگی، شکرگزار پروردگار باش و از دستگیری دردمندان غافل مشو تا روشنایی بختت پایدار بماند.",
	2141: "ای صاحب فال! دلی شیدا و آرزویی لطیف در سر داری. از هیاهو و فتنه‌های محیط فاصله بگیر و بر هدف اصیل خود متمرکز شو. راستی و پاکی درون تو را به مقصود خواهد رساند.",
	2142: "ای صاحب فال! در کار عشق و زندگی، از فداکاری و ایثار نهراس. آن‌که جان و همت خود را خالصانه فدای مقصود کند، پاداشی شایسته و ابدی دریافت خواهد کرد. به عهد و پیمان خویش استوار بمان.",
	2143: "ای صاحب فال! گشایشی که منتظر آن بودی، آرام‌آرام جلوه‌گر می‌شود. قدردان محبت‌ها باش و مگذار رنجش‌های کوچک میان تو و یارانت جدایی بیندازد. چشم به راه آینده‌ای روشن باش.",
	2144: "ای صاحب فال! سختی‌هایی که تحمل می‌کنی، سبب جلا و پختگی روحت است. چون زر ناب که در آتش پاکیزه می‌شود، صبوری تو نیز سرانجام عزت و سربلندی به بار خواهد آورد. امید را چراغ راهت دار.",
	2145: "ای صاحب فال! در مقابل وسوسه‌ها و فریب‌های ظاهری هشیار باش. همه‌چیز آن‌گونه که به نظر می‌رسد نیست. با خرد و فراست تصمیم بگیر و عنان اختیار خود را به دست احساسات زودگذر مسپار.",
	2146: "ای صاحب فال! بر عهد و وفای خود پایدار باش و به وسوسه تغییر مسیر زودهنگام دل مسپار. وفاداری و پایداری در دوستی و کار، نشان از اصالت تو دارد و سرانجام مقصود را به آغوشت خواهد سپرد.",
	2147: "ای صاحب فال! مژده وصل و دیدار نزدیک است. بگذار غم هجران و تنهایی به فراموشی سپرده شود. کینه‌ها را از سینه بیرون بریز و با دلی شاداب به استقبال روزهای نو برو.",
	2148: "ای صاحب فال! راهی که انتخاب کرده‌ای در آغاز سنگلاخ است، اما با همت و استقامت هموار خواهد شد. از سرزنش بدخواهان نهراس و دل به تقدیر نیکوی الهی خوش دار.",
	2149: "ای صاحب فال! کارها بر وفق مرادت پیش خواهد رفت، به شرط آنکه قدر یاران مخلص را بدانی و در شادی و غم شریکشان باشی. به خدای مهربان توکل کن و صدقه‌ای بده.",
	2151: "ای صاحب فال! در کاری که در نظر داری، صبر تلخ است اما ثمری شیرین به همراه دارد. از فراز و نشیب روزگار هراسی به دل راه مده؛ زیرا دست عنایت الهی گره‌گشای کارهاست. رازت را پوشیده دار و به وسوسه کوتاه‌مدت ناامیدی تسلیم مشو.",
	2155: "ای صاحب فال! شادمانی و کامروایی غیرمنتظره‌ای به سراغت خواهد آمد. بندهای اسارت و دلمردگی گسسته شده و نسیم فرح‌بخش طرب در خانه‌ات وزیده است. از این نشاط برای انگیزه و ساختن آینده‌ای درخشان سود ببر.",
	2161: "ای صاحب فال! اگر در جستجوی معشوق یا کاری ارزشمند هستی، دست از ملامت خویشتن بردار. شجاعت داشته باش و با قلبی پرشور در میدان عمل گام بگذار. ظفر از آن دل‌های پاک و بااراده است.",
	2173: "ای صاحب فال! نیت خیری در سر داری و در طلب مقصودی ارزشمند هستی. دل به وعده‌های توخالی مبند و چشم امید به غیر خدا مدوز. با تلاش صادقانه و توکل به قادر متعال، ابرهای تیره پراکنده شده و خورشید پیروزی بر بامت خواهد درخشید.",
	2200: "ای صاحب فال! از خرده‌گیری و طعنه‌های حسودان و نااهلان اندوهگین مباش؛ چرا که پاکی نیت تو بهترین پناهگاهت است. کار خود را به خدا بسپار و با دلی روشن به مسیر حق ادامه ده. رحمت بی‌پایان حق پشتیبان توست و بدخواهان خود شرمسار خواهند شد.",
	2223: "ای صاحب فال! پیوندی فرخنده یا دیداری دل‌انگیز در تقدیر تو رقم خورده است. قدر همدمان و یاران موافق را بدان و از خطاهای گذشته چشم‌پوشی کن. با مهربانی و وفاداری بذر مهر بیفشان تا خوشبختی و آرامش در زندگیت ریشه بدواند.",
	2255: "ای صاحب فال! جان و روحت در طلب صفا و کمال است. دل از دنیاپرستی و علایق پست برکن تا زیبایی‌های اصیل عالم برایت متجلی شود. در راه معرفت و عشق حقیقی گام بردار تا به سرچشمه نشاط برسی.",
	2265: "ای صاحب فال! در راه رسیدن به مقصود همتی بلند داری و اراده‌ات ستودنی است. با این حال بدان که این کار نیازمند صبر، استقامت و گذشت است. سختی‌های مسیر را چون پلکانی برای رشد روحی خود ببین و مطمئن باش که پاداش این پایداری، ظفر و سربلندی خواهد بود.",
	2272: "ای صاحب فال! گوهر مقصود و کلید حل مشکل در درون خود تو نهفته است، اما تو آن را در بیرون جستجو می‌کنی. به آرامش و بینش باطنی روی بیاور و از شتاب و ظاهرپرستی دوری کن. با تکیه بر آگاهی و خرد الهی، حجاب‌ها کنار رفته و به حقیقت دست خواهی یافت.",
	2296: "ای صاحب فال! ستاره اقبالت درخشیدن گرفته و دورانی نو و پرامید آغاز شده است. شایستگی و درایت تو سرانجام بر همگان آشکار خواهد شد. از فرصت‌های پیش‌رو به نیکی بهره ببر، تواضع خود را حفظ کن و با توکل به پروردگار به مدارج عالی بیندیش.",
	2300: "ای صاحب فال! پیک بشارت و مژده‌ای شیرین به زودی در خانه‌ات را خواهد کوبید. رنج و ملال دوران گذشته جایش را به عیش و شادمانی خواهد داد. شکر نعمت به جای آور و دلت را به رحمت واسعه الهی بسپار.",
	2313: "ای صاحب فال! بار امانتی سنگین اما مقدس بر دوش تو نهاده شده است. روح و جوهره وجودت شایسته رسیدن به کمالات والاست. از رنج راه مگریز؛ چرا که گوهر عشق و معرفت در کوره سختی‌ها جلا می‌یابد. به عهد خود وفادار بمان تا الطاف پروردگار شامل حالت باشد.",
	2322: "ای صاحب فال! رفتار و منش تو برای کسانی که سطحی‌نگرند شاید قابل فهم نباشد؛ پس مگذار طعنه دیگران تو را از آرمان‌هایت بازدارد. دلت را به نور محبت و راستی زنده نگه دار و از قضاوت‌های ناعادلانه پروا مکن.",
	2353: "ای صاحب فال! نسیم صفا و آسایش روحی به سوی تو وزیدن گرفته است. قدردان زیبایی‌ها، سلامت و آرامش حاضر باش و در جمع نیکان و اهل هنر روزگار بگذران. با نشاط و امید زندگی کن و بدان که روزهای دل‌انگیز در انتظار توست.",
	2359: "ای صاحب فال! در راه وصال به مقصود، تندباد حوادث نباید پای اراده‌ات را سست کند. همت کن و به جلو پیش برو. پیروزی با کسانی است که از موانع پلی برای موفقیت می‌سازند.",
	2373: "ای صاحب فال! مژده باد که گره‌های فروبسته در حال گشایش است و مسیحای امید و سلامت از راه می‌رسد. بیماری یا غم به نشاط بدل خواهد شد. ناامیدی را از دل بیرون کن و در هر سحرگاه دست به دعا بردار که استجابت نزدیک است.",
	2384: "ای صاحب فال! یوسف گمگشتهٔ امید و آرامش به سوی تو بازخواهد گشت؛ غم مخور و دل قوی دار. این دوران سختی و فراق پایدار نخواهد بود و دگرگونی‌های خجسته در پیش است. کلید این پیروزی، صبر و تسلیم در برابر مشیت الهی است؛ پس امیدت را هرگز از دست مده.",
	2446: "ای صاحب فال! با شجاعت و آزادگی از حقیقت دفاع کن و نگران قضاوت‌های ناصواب مباش. گوهر آزادگی و رندی در پرتو پاکی دل و دوری از تزویر به دست می‌آید. به ذات اقدس الهی اعتماد کن و بدان که پاداش راستی و پایمردی، عزتی جاودانه خواهد بود.",
	2450: "ای صاحب فال! هرچند بار خستگی و روزگار گذشته بر شانه‌هایت سنگینی می‌کند، اما عشقی تازه یا انگیزه‌ای نو در دلت جان خواهد گرفت و جوانی و طراوت را به تو بازخواهد گرداند. به آینده خوش‌بین باش.",
	2461: "ای صاحب فال! از طعنه‌ها و جفای روزگار شکوه مکن؛ چرا که در پس هر ملامتی، رمزی از رشد و پختگی نهفته است. در عشق و اهدافت ثابت‌قدم بمان و مگذار وسوسه‌ها دلت را به لرزه درآورند.",
	2479: "ای صاحب فال! دلباخته و شیدای مقصودی شده‌ای که تمام فکر و خیالت را ربوده است. مراقب باش که تعادل زندگی و کار را از دست ندهی. با درایت، متانت و تدبیر گام بردار تا محبوبت را راضی و خشنود سازی.",
	2489: "ای صاحب فال! اگر دلت از جفای یاری گرفته، با بزرگواری و گذشت پاسخ بده. نیکی کردن به بدخواهان از کرامت روح تو سرچشمه می‌گیرد و دشمنان را شرمسار و دوستان را پایدار می‌کند.",
	2529: "ای صاحب فال! همتی والا در سر داری و در پی هدفی شکوهمند هستی. با انسان‌های صاحب‌نظر و دانا مشورت کن و سرمایه عمرت را در راه امور کوچک هدر مده. با توکل به خدا و تلاش پیوسته، دست یافتن به اوج ممکن خواهد بود.",
	2600: "ای صاحب فال! چشم‌انتظار خبری یا پیامی از یاری دوردست هستی. به زودی قاصد خوش‌خبر پیامی مسرت‌بخش برایت خواهد آورد. در این ایام انتظار، دل را به امید گرم دار و از بی‌قراری بپرهیز.",
	2622: "ای صاحب فال! از تنهایی، فراق یا غم دلتنگ شده‌ای و روزگار را بر خود سخت می‌گیری. بدان که این شب هجران رو به سپیده است و دیدار و وصال محبوب نزدیک. با صبر و توکل دلت را آرام کن، از بی‌قراری بپرهیز و به یاد داشته باش که روزهای شادمانه در راهند.",
	2624: "ای صاحب فال! زندگی کوتاه است و غم خوردن برای امور گذرا سزاوار تو نیست. شادی و امید را در دل زنده نگه دار و گلستان مهربانی را بارور ساز. با بخشش و نیکی نام خود را جاودان کن و توکل به خدای بخشاینده را لحظه‌ای فراموش منما.",
}

type FaalTheme struct {
	Keywords []string
	Openings []string
	Wisdoms  []string
	Advices  []string
}

var faalThemes = map[string]FaalTheme{
	"joy": {
		Keywords: []string{"مژده", "بشارت", "کام", "ظفر", "فتح", "پیروز", "طرب", "عیش", "شادی", "خندان", "خرم", "بهار", "گلستان", "بخت", "دولت", "اقبال"},
		Openings: []string{
			"ای صاحب فال! بشارت باد که روزهای خجستگی و گشایش نزدیک است و چرخ روزگار به مراد دلت خواهد گشت.",
			"ای صاحب فال! فال تو سرشار از نوید کامروایی و پیروزی است؛ ابواب بسته پیش پایت باز خواهد شد.",
			"ای صاحب فال! مژده باد که دوران عسرت رو به پایان است و اقبال و سعادت به استقبالت آمده است.",
		},
		Wisdoms: []string{
			"این غزل نویدبخش وصال، پیروزی و رهایی از بند دغدغه‌هاست. دولت بیدار و بخت همراهت شده تا نتیجه زحمات گذشته‌ات را ببینی.",
			"حکمت این کلام در پیوند مبارک میان نیت پاک تو و عنایت الهی است. شادی حقیقی زمانی رخ می‌دهد که دل از غم و کینه پیراسته شود.",
			"این ابیات خجسته نشان از آن دارد که موانع یکی پس از دیگری برداشته می‌شود و روشنایی بر سراپرده زندگیت می‌تابد.",
		},
		Advices: []string{
			"شکرانه این موهبت را با احسان به دیگران و دستگیری از افتادگان به جای آور و از سرمستی و غرور پرهیز کن تا این دولت پایدار بماند.",
			"از بخل و حسادت دوری کن، با روی گشاده با مردمان برخورد نما و در شادی دیگران را نیز سهیم دار.",
			"به خداوند توکل کن، صدقه‌ای در راه خدا بده و قدر نعمت‌های امروز را بدان تا برکت زندگیت فزونی یابد.",
		},
	},
	"hope": {
		Keywords: []string{"سحر", "صبح", "آفتاب", "خورشید", "چراغ", "شمع", "روشن", "نور", "تابان", "فلق", "امید", "سپیده"},
		Openings: []string{
			"ای صاحب فال! نور امید در افق نیت تو درخشیده و تیرگی شب تردید و اضطراب رو به پایان است.",
			"ای صاحب فال! سپیده‌دم روشنایی و آگاهی بر تاریکی مشکلاتت پیروز شده و راهت آشکار و هموار می‌گردد.",
			"ای صاحب فال! خورشید سعادت بر بام سرنوشتت طلوع کرده و زمستان دلتنگیت به بهاری دل‌انگیز بدل خواهد شد.",
		},
		Wisdoms: []string{
			"حکمت این کلام در دمیدن سحرگاه آرامش پس از شبی دراز است. اگر دل به روشنایی حقیقت بسپاری، راه‌های بسته پیش پایت گشوده خواهد شد.",
			"این غزل پیام‌آور امید و آگاهی است؛ چراغ خرد و اخلاص تاریک‌ترین مسیرها را روشن خواهد کرد.",
			"ابیات پیش‌رو به تو یادآور می‌شود که هر آغاز نو نیازمند گذر از ظلمات بیم و اضطراب است و تو اکنون در آستانه روشنایی ایستاده‌ای.",
		},
		Advices: []string{
			"سحرخیز باش و دل در گرو دعا و یاد حق دار؛ از نومیدی فاصله بگیر که سپیده‌دم پیروزی نزدیک‌تر از آن است که می‌پنداری.",
			"امیدت را هرگز از دست مده، با اشتیاق و انرژی مثبت به استقبال امور برو و افکار منفی را از خود دور ساز.",
			"دست از طلب مدار، با یاران باایمان همگام شو و نور امید را در دل اطرافیانت نیز روشن نگه دار.",
		},
	},
	"patience": {
		Keywords: []string{"غم", "محنت", "رنج", "اندوه", "درد", "اشک", "صبر", "شکیب", "فراق", "هجران", "بلا", "خون"},
		Openings: []string{
			"ای صاحب فال! اگر چه در دل احساس تنگی و اندوه داری، اما بدان که هیچ شبی جاودانه نیست و رنج تو بی‌پاداش نخواهد ماند.",
			"ای صاحب فال! روزگاری سخت یا امتحانی دشوار را پشت سر می‌گذاری، اما گشایش و پیروزی در یک قدمی توست.",
			"ای صاحب فال! از تلخی ایام و فراق دل‌شکسته مباش؛ چرا که روزهای شیرین وصال و آسایش در پیش است.",
		},
		Wisdoms: []string{
			"پیام این غزل شکیبایی در برابر سختی‌ها و پالایش دل در کوره تجارب است. مروارید صفا در صدف همین روزهای دشوار پرورده می‌شود.",
			"این کلام حافظ داروی شفابخش دل‌های سوخته است؛ صبوری بر ناملایمات، ریشه اضطراب را می‌سوزاند و آرامشی عمیق می‌بخشد.",
			"حکمت این غزل در تسلیم‌ناپذیری روح و امیدواری به لطف پنهان الهی است؛ هر گرهی که محکم‌تر شود، گشایشش نزدیک‌تر است.",
		},
		Advices: []string{
			"عنان صبر از دست مده و در تصمیم‌هایت شتاب مکن. به وعده الهی ایمان داشته باش که پس از هر دشواری، آسانی و فرجی بزرگ خواهد بود.",
			"راز دلت را با نامحرمان مگوی، با شکیبایی به اصلاح امور بپرداز و از شکایت و بی‌تابی بپرهیز.",
			"به خداوند منان پناه ببر، استقامت پیشه کن و مطمئن باش که پاداش این صبوری، سرور و سربلندی خواهد بود.",
		},
	},
	"integrity": {
		Keywords: []string{"ریا", "زاهد", "صومعه", "خرقه", "تزویر", "ملامت", "طعنه", "حسود", "بدگو", "عیب", "طامات"},
		Openings: []string{
			"ای صاحب فال! از سرزنش دیگران و داوری‌های ظاهربینانه هراسی به خود راه مده؛ پاکی نیت بهترین زره توست.",
			"ای صاحب فال! در میان حسادت‌ها و بدگویی‌های زمانه، راه صدق و صفا را گم مکن و اصالت خود را حفظ نما.",
			"ای صاحب فال! نیت تو پاک و بی‌آلایش است؛ مگذار حرف‌های بی‌اساس مردم آرامش درونت را بر هم زند.",
		},
		Wisdoms: []string{
			"این ابیات تو را به صدق باطن و دوری از تظاهر و خودنمایی فرا می‌خواند. آنچه برای تو رستگاری می‌آورد، صفای درون و یکرنگی با خویشتن و پروردگار است.",
			"حکمت کلام در افشای فریب‌های ظاهری و روی آوردن به جوهره راستی است؛ کسانی که به ظاهر داوری می‌کنند، از درک حقیقت عاجزند.",
			"این غزل گواهی می‌دهد که رهایی از ریا و خودبینی، بزرگ‌ترین گام به سوی تعالی و سعادت روح است.",
		},
		Advices: []string{
			"راز دلت را با هر کسی در میان مگذار و از معاشرت با انسان‌های دورو بپرهیز. با توکل بر خدا راه راست خود را بدون توجه به ملامت‌گران پی بگیر.",
			"با خویشتن و دیگران صادق باش، از ریا و تظاهر دوری کن و امور خود را با درایت و سکوت پیش ببر.",
			"به قضاوت‌های ناعادلانه بی‌اعتنا باش، به خدا تکیه کن و اجازه نده بدخواهان تو را از مسیر درست منحرف کنند.",
		},
	},
	"trust": {
		Keywords: []string{"توکل", "رضا", "تقدیر", "عنایت", "فضل", "لطف", "کرم", "رحمت", "دعا", "حق", "خدا", "ایزد"},
		Openings: []string{
			"ای صاحب فال! نیت تو از سر صدق است و رشته کارت به دست لطف و عنایت پروردگار سپرده شده است.",
			"ای صاحب فال! بار اضطراب را از شانه بردار؛ خیری بزرگ و الطافی پنهان از جانب حق در انتظار توست.",
			"ای صاحب فال! در کاری که برگزیده‌ای به قدرت لایزال الهی تکیه کرده‌ای و بدان که پشتیبانی استوارتر از او نخواهی یافت.",
		},
		Wisdoms: []string{
			"این غزل سرشار از آموزه تسلیم و رضا در برابر حکمت الهی است. آن‌کس که کار خود را به خداوند بسپارد، هرگز در بن‌بست درماندگی نخواهد ماند.",
			"پیام این کلام ژرف، ایمان به نظم و عنایت پروردگار است؛ چه بسا امری که ناخوشایند می‌پنداری، سرآغاز خیر و برکتی عظیم باشد.",
			"حکمت ابیات در آرامش دل از طریق پیوند با سرچشمه فضل و رحمت بی‌پایان الهی نهفته است.",
		},
		Advices: []string{
			"نگرانی‌های بیهوده را از روانت دور ساز، با اطمینان قلبی پیش برو و بدان که تدبیر خداوند همواره فراتر و نیکوتر از تدبیر بندگان است.",
			"دست دعا بردار، با قلبی مالامال از خلوص از پروردگار هدایت بطلب و از تکیه بر اسباب ناپایدار دنیوی بپرهیز.",
			"رضایت حق را در همه امور سرلوحه قرار بده، به وعده‌های الهی اعتماد کن و خیرخواه دیگران باش تا خیر به سویت بازگردد.",
		},
	},
	"love": {
		Keywords: []string{"مهر", "وفا", "عهد", "پیمان", "وفادار", "جانان", "حبیب", "یار", "عشق", "معشوق", "دوست", "صفا"},
		Openings: []string{
			"ای صاحب فال! در راهی که برگزیده‌ای، عشق، همدلی و پایبندی به عهد مهم‌ترین سرمایه تو به شمار می‌آید.",
			"ای صاحب فال! پیوندی فرخنده یا تجدید مهری از یاد رفته، جانت را شاداب و دلت را گرم خواهد ساخت.",
			"ای صاحب فال! نیت تو سرشار از پاکی و محبت است؛ پاسخی درخور و شیرین به این مهربانی دریافت خواهی کرد.",
		},
		Wisdoms: []string{
			"این کلام از اکسیر محبت و وفاداری سخن می‌گوید. با گشاده‌رویی و وفای به عهد می‌توانی حتی دل‌های سخت را نرم و همراه خود سازی.",
			"حکمت این غزل در پاسداشت ارزش دوستی‌های اصیل و گذشت از خطاهای کوچک در راه الفت و همبستگی است.",
			"ابیات پیش‌رو یادآور می‌شوند که عشق و صداقت زنده است و هر دلی که به نور محبت روشن شود، جاودانه خواهد ماند.",
		},
		Advices: []string{
			"قدر یاران موافق و همراهان باصفایت را بدان، کینه‌ها را از دل بزدا و با مهرورزی بی چشمداشت، مسیر خود را هموار ساز.",
			"در عهد و پیمان خود استوار بمان، از لجاجت و غرور دوری کن و زبان به ملامت مگشای.",
			"با دوستانت با رفق و مدارا رفتار کن، محبت را نثار کن و مطمئن باش که عشق حقیقی راهگشای همه دشواری‌هاست.",
		},
	},
	"perseverance": {
		Keywords: []string{"سفر", "راه", "منزل", "کاروان", "همت", "طلب", "سعی", "قدم", "مقصد", "هادی"},
		Openings: []string{
			"ای صاحب فال! نیت تو نیازمند حرکتی شجاعانه و تلاشی پیگیر است؛ زمان ایستایی سپری شده و باید پای در راه بگذاری.",
			"ای صاحب فال! هدفی بلند و ارزشمند در سر داری؛ با عزم راسخ و پرهیز از تردید قدم بردار که رسیدن ممکن است.",
			"ای صاحب فال! در آستانه تصمیمی مهم و حرکتی سرنوشت‌ساز هستی؛ توشه این راه ایمان، تدبیر و خستگی‌ناپذیری است.",
		},
		Wisdoms: []string{
			"این غزل یادآور آن است که بدون تلاش و همت والا، دستیابی به مقاصد ارزشمند میسر نمی‌شود. سختی‌های مسیر را به جان بخر که منزل مقصود نزدیک است.",
			"حکمت این کلام در پیوستگی تلاش و خردمندی در طول مسیر است؛ کاروان موفقیت در انتظار کسانی است که از موانع نهراسند.",
			"ابیات این غزل نوید می‌دهند که هر قدم صادقانه‌ای که برمی‌داری، تو را به مقصود نزدیک‌تر می‌کند و راهنمای غیب همراهت خواهد بود.",
		},
		Advices: []string{
			"توشه راهت را با خرد و مشورت فراهم کن، از تردید بر حذر باش و با عزمی استوار به سوی آرمان‌هایت گام بردار.",
			"سستی و تنبلی را کنار بگذار، با برنامه و مشورت پیش برو و در میانه راه دچار یاس و تزلزل مشو.",
			"همت خود را بلند دار، به خداوند توکل کن و با ثبات قدم مسیر تعالی و رشد خود را دنبال نما.",
		},
	},
}

func generateThematicInterpretation(poem *PoemDetail, verses []VerseGroup) string {
	var sb strings.Builder
	for _, vg := range verses {
		for _, vi := range vg.Items {
			sb.WriteString(vi.Text)
			sb.WriteString(" ")
		}
	}
	allText := sb.String()

	themeKeys := []string{"joy", "hope", "patience", "integrity", "trust", "love", "perseverance"}
	type scoredTheme struct {
		key   string
		score int
	}
	scores := make([]scoredTheme, 0, len(themeKeys))

	for _, key := range themeKeys {
		th := faalThemes[key]
		score := 0
		for _, kw := range th.Keywords {
			score += strings.Count(allText, kw)
		}
		scores = append(scores, scoredTheme{key: key, score: score})
	}

	// Sort descending by score
	for i := 0; i < len(scores)-1; i++ {
		for j := i + 1; j < len(scores); j++ {
			if scores[j].score > scores[i].score {
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}

	topKey := scores[0].key
	secKey := scores[1].key
	if scores[1].score == 0 {
		secKey = "trust"
		if topKey == "trust" {
			secKey = "hope"
		}
	}

	topTheme := faalThemes[topKey]
	secTheme := faalThemes[secKey]

	idx1 := int(poem.PoemID) % len(topTheme.Openings)
	idx2 := int(poem.PoemID/7) % len(secTheme.Wisdoms)
	idx3 := int(poem.PoemID/13) % len(topTheme.Advices)

	return fmt.Sprintf("%s\n\n%s\n\n%s", topTheme.Openings[idx1], secTheme.Wisdoms[idx2], topTheme.Advices[idx3])
}

func getFaalInterpretation(poem *PoemDetail, verses []VerseGroup) string {
	if interp, ok := curatedFaalInterpretations[poem.PoemID]; ok && interp != "" {
		return interp
	}
	return generateThematicInterpretation(poem, verses)
}

// 12. Ceremonial Faal-e Hafez
func handleFaal(w http.ResponseWriter, r *http.Request) {
	// Prevent aggressive caching so 'فال مجدد' reliably returns fresh results
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	var poemID int64
	if reqIDStr := r.URL.Query().Get("id"); reqIDStr != "" {
		if parsed, err := strconv.ParseInt(reqIDStr, 10, 64); err == nil && parsed > 0 {
			poemID = parsed
		}
	}
	if poemID == 0 {
		if reqIDStr := r.URL.Query().Get("poem_id"); reqIDStr != "" {
			if parsed, err := strconv.ParseInt(reqIDStr, 10, 64); err == nil && parsed > 0 {
				poemID = parsed
			}
		}
	}

	if poemID == 0 {
		ids, err := getHafezGhazalIDs()
		if err != nil || len(ids) == 0 {
			http.Error(w, "Failed to load Hafez ghazals", http.StatusInternalServerError)
			return
		}
		// Pick random ID
		poemID = ids[rand.Intn(len(ids))]
	}

	poem, verses, poemIDs, err := fetchPoemWithVerses(poemID)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Ghazal not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	interpretation := getFaalInterpretation(poem, verses)

	writeJSON(w, http.StatusOK, FaalResponse{
		Poem:           poem,
		Verses:         verses,
		Interpretation: interpretation,
		PoemIDs:        poemIDs,
	})
}

// 10. Unified Fast Search (FTS5 + B-Tree)
func handleSearch(w http.ResponseWriter, r *http.Request) {
	rawQ := r.URL.Query().Get("q")
	q := normalizePersian(rawQ)
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "all"
	}
	poetID := r.URL.Query().Get("poet")
	century := r.URL.Query().Get("century")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	res := SearchResponse{
		Poets:  []Poet{},
		Works:  []WorkItem{},
		Poems:  []SearchPoemResult{},
		Verses: []SearchVerseResult{},
	}

	ftsExpr := buildFTSMatchExpr(q)
	likeQ := "%" + foldAlefLike(q) + "%"

	// A. Search Poets
	if (scope == "all" || scope == "poets") && (q != "" || century != "") {
		var where []string
		var args []interface{}
		if q != "" {
			where = append(where, fmt.Sprintf("(%s LIKE ? OR %s LIKE ?)", sqlNormCol("name"), sqlNormCol("birthplace")))
			args = append(args, likeQ, likeQ)
		}
		if century != "" {
			if century == "__other__" {
				where = append(where, "century_id IS NULL")
			} else {
				where = append(where, "CAST(century_id AS TEXT) = ?")
				args = append(args, century)
			}
		}
		pquery := fmt.Sprintf(`SELECT id, name, cat_id, description, century_id, birth_year, death_year, birthplace, deathplace 
			FROM poet WHERE %s ORDER BY name COLLATE NOCASE LIMIT %d`, strings.Join(where, " AND "), limit)
		rows, err := db.Query(pquery, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var p Poet
				if err := rows.Scan(&p.ID, &p.Name, &p.CatID, &p.Description, &p.CenturyID, &p.BirthYear, &p.DeathYear, &p.Birthplace, &p.Deathplace); err == nil {
					res.Poets = append(res.Poets, p)
				}
			}
		}
	}

	// B. Search Works
	if (scope == "all" || scope == "works") && (q != "" || poetID != "" || century != "") {
		var where []string
		var args []interface{}
		if q != "" {
			where = append(where, fmt.Sprintf("(%s LIKE ? OR %s LIKE ?)", sqlNormCol("c.text"), sqlNormCol("p.name")))
			args = append(args, likeQ, likeQ)
		}
		if poetID != "" {
			where = append(where, "p.id = ?")
			args = append(args, poetID)
		}
		if century != "" {
			if century == "__other__" {
				where = append(where, "p.century_id IS NULL")
			} else {
				where = append(where, "CAST(p.century_id AS TEXT) = ?")
				args = append(args, century)
			}
		}
		wquery := fmt.Sprintf(`SELECT c.id AS cat_id, c.text AS title, p.name AS poet_name, p.birth_year, p.birthplace, p.century_id, c.parent_id
			FROM cat c JOIN poet p ON p.id=c.poet_id JOIN cat root ON root.id=c.parent_id AND root.parent_id=0
			WHERE %s ORDER BY c.text COLLATE NOCASE LIMIT %d`, strings.Join(where, " AND "), limit)
		rows, err := db.Query(wquery, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var item WorkItem
				if err := rows.Scan(&item.CatID, &item.Title, &item.PoetName, &item.BirthYear, &item.Birthplace, &item.CenturyID, &item.ParentID); err == nil {
					res.Works = append(res.Works, item)
				}
			}
		}
	}

	// C. Search Poems (using poem_fts if available)
	if (scope == "all" || scope == "poems") && (q != "" || poetID != "" || century != "") {
		var where []string
		var args []interface{}
		var fromClause string

		if ftsExpr != "" {
			fromClause = "poem_fts f JOIN poem po ON po.id = f.rowid JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id"
			where = append(where, "poem_fts MATCH ?")
			args = append(args, ftsExpr)
		} else {
			fromClause = "poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id"
		}

		if poetID != "" {
			where = append(where, "p.id = ?")
			args = append(args, poetID)
		}
		if century != "" {
			if century == "__other__" {
				where = append(where, "p.century_id IS NULL")
			} else {
				where = append(where, "CAST(p.century_id AS TEXT) = ?")
				args = append(args, century)
			}
		}

		poemQuery := fmt.Sprintf(`SELECT po.id AS poem_id, po.title, p.name AS poet_name, c.text AS cat_title, p.century_id
			FROM %s WHERE %s LIMIT %d`, fromClause, strings.Join(where, " AND "), limit)
		rows, err := db.Query(poemQuery, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var item SearchPoemResult
				if err := rows.Scan(&item.PoemID, &item.Title, &item.PoetName, &item.CatTitle, &item.CenturyID); err == nil {
					res.Poems = append(res.Poems, item)
				}
			}
		} else if ftsExpr != "" {
			// Fallback to LIKE if FTS table is not yet generated
			whereFallback := []string{fmt.Sprintf("%s LIKE ?", sqlNormCol("po.title"))}
			argsFallback := []interface{}{likeQ}
			if poetID != "" {
				whereFallback = append(whereFallback, "p.id = ?")
				argsFallback = append(argsFallback, poetID)
			}
			if century != "" {
				whereFallback = append(whereFallback, "CAST(p.century_id AS TEXT) = ?")
				argsFallback = append(argsFallback, century)
			}
			fallbackQuery := fmt.Sprintf(`SELECT po.id AS poem_id, po.title, p.name AS poet_name, c.text AS cat_title, p.century_id
				FROM poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id WHERE %s LIMIT %d`, strings.Join(whereFallback, " AND "), limit)
			frows, ferr := db.Query(fallbackQuery, argsFallback...)
			if ferr == nil {
				defer frows.Close()
				for frows.Next() {
					var item SearchPoemResult
					if err := frows.Scan(&item.PoemID, &item.Title, &item.PoetName, &item.CatTitle, &item.CenturyID); err == nil {
						res.Poems = append(res.Poems, item)
					}
				}
			}
		}
	}

	// D. Search Verses (using verse_fts for sub-5ms search, returning full couplet text when paired)
	if (scope == "all" || scope == "verses") && utf8.RuneCountInString(q) >= 2 {
		var where []string
		var args []interface{}
		verseLimit := limit
		if verseLimit < 50 {
			verseLimit = 50
		}

		if ftsExpr != "" {
			where = append(where, "verse_fts MATCH ?")
			args = append(args, ftsExpr)
			if poetID != "" {
				where = append(where, "p.id = ?")
				args = append(args, poetID)
			}
			if century != "" {
				if century == "__other__" {
					where = append(where, "p.century_id IS NULL")
				} else {
					where = append(where, "CAST(p.century_id AS TEXT) = ?")
					args = append(args, century)
				}
			}

			vquery := fmt.Sprintf(`
				SELECT v.poem_id,
					CASE WHEN v.position IN (1, 3) AND vp.vorder IS NOT NULL THEN vp.vorder ELSE v.vorder END AS couplet_vorder,
					v.position,
					CASE
						WHEN v.position IN (0, 2) AND vn.text IS NOT NULL THEN v.text || ' / ' || vn.text
						WHEN v.position IN (1, 3) AND vp.text IS NOT NULL THEN vp.text || ' / ' || v.text
						ELSE v.text
					END AS full_text,
					po.title, p.name AS poet_name, p.id AS poet_id, p.century_id, c.text AS cat_title
				FROM verse_fts f
				JOIN verse v ON v.rowid = f.rowid
				LEFT JOIN verse vn ON vn.poem_id = v.poem_id AND vn.vorder = v.vorder + 1 AND ((v.position = 0 AND vn.position = 1) OR (v.position = 2 AND vn.position = 3))
				LEFT JOIN verse vp ON vp.poem_id = v.poem_id AND vp.vorder = v.vorder - 1 AND ((v.position = 1 AND vp.position = 0) OR (v.position = 3 AND vp.position = 2))
				JOIN poem po ON po.id = v.poem_id
				JOIN cat c ON c.id = po.cat_id
				JOIN poet p ON p.id = c.poet_id
				WHERE %s LIMIT %d`, strings.Join(where, " AND "), verseLimit)

			rows, err := db.Query(vquery, args...)
			if err == nil {
				defer rows.Close()
				seenCouplets := make(map[string]bool)
				for rows.Next() {
					var item SearchVerseResult
					if err := rows.Scan(&item.PoemID, &item.VOrder, &item.Position, &item.Text, &item.Title, &item.PoetName, &item.PoetID, &item.CenturyID, &item.CatTitle); err == nil {
						key := fmt.Sprintf("%d:%d", item.PoemID, item.VOrder)
						if !seenCouplets[key] {
							seenCouplets[key] = true
							res.Verses = append(res.Verses, item)
						}
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, res)
}

func capInt64Slice(s []int64, max int) []int64 {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// 11. Batch Bookmarks Resolver
func handleBookmarks(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req BookmarkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid bookmark request body", http.StatusBadRequest)
		return
	}
	req.Poets = capInt64Slice(req.Poets, 500)
	req.Works = capInt64Slice(req.Works, 500)
	req.Poems = capInt64Slice(req.Poems, 500)
	if len(req.Verses) > 500 {
		req.Verses = req.Verses[:500]
	}

	resp := BookmarkResponse{
		Poets:  []Poet{},
		Works:  []WorkItem{},
		Poems:  []SearchPoemResult{},
		Verses: []SearchVerseResult{},
	}

	// 1. Resolve Poets
	if len(req.Poets) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(req.Poets)), ",")
		args := make([]interface{}, len(req.Poets))
		for i, id := range req.Poets {
			args[i] = id
		}
		rows, err := db.Query(fmt.Sprintf(`SELECT id, name, cat_id, description, century_id, birth_year, death_year, birthplace, deathplace 
			FROM poet WHERE id IN (%s)`, placeholders), args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var p Poet
				if err := rows.Scan(&p.ID, &p.Name, &p.CatID, &p.Description, &p.CenturyID, &p.BirthYear, &p.DeathYear, &p.Birthplace, &p.Deathplace); err == nil {
					resp.Poets = append(resp.Poets, p)
				}
			}
		}
	}

	// 2. Resolve Works
	if len(req.Works) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(req.Works)), ",")
		args := make([]interface{}, len(req.Works))
		for i, id := range req.Works {
			args[i] = id
		}
		rows, err := db.Query(fmt.Sprintf(`SELECT c.id AS cat_id, c.text AS title, p.name AS poet_name, p.birth_year, p.birthplace, p.century_id, c.parent_id
			FROM cat c JOIN poet p ON p.id=c.poet_id WHERE c.id IN (%s)`, placeholders), args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var item WorkItem
				if err := rows.Scan(&item.CatID, &item.Title, &item.PoetName, &item.BirthYear, &item.Birthplace, &item.CenturyID, &item.ParentID); err == nil {
					resp.Works = append(resp.Works, item)
				}
			}
		}
	}

	// 3. Resolve Poems
	if len(req.Poems) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(req.Poems)), ",")
		args := make([]interface{}, len(req.Poems))
		for i, id := range req.Poems {
			args[i] = id
		}
		rows, err := db.Query(fmt.Sprintf(`SELECT po.id AS poem_id, po.title, p.name AS poet_name, c.text AS cat_title, p.century_id
			FROM poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id WHERE po.id IN (%s)`, placeholders), args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var item SearchPoemResult
				if err := rows.Scan(&item.PoemID, &item.Title, &item.PoetName, &item.CatTitle, &item.CenturyID); err == nil {
					resp.Poems = append(resp.Poems, item)
				}
			}
		}
	}

	// 4. Resolve Verses (format "poem_id:vorder", hydrating both hemistichs of a couplet)
	if len(req.Verses) > 0 {
		var whereParts []string
		var args []interface{}
		for _, vKey := range req.Verses {
			parts := strings.Split(vKey, ":")
			if len(parts) >= 2 {
				pid, _ := strconv.ParseInt(parts[0], 10, 64)
				vorder, _ := strconv.ParseInt(parts[1], 10, 64)
				if pid > 0 {
					whereParts = append(whereParts, "(v.poem_id = ? AND v.vorder = ?)")
					args = append(args, pid, vorder)
				}
			} else if len(parts) == 1 {
				pid, _ := strconv.ParseInt(parts[0], 10, 64)
				if pid > 0 {
					whereParts = append(whereParts, "(v.poem_id = ? AND v.vorder = 1)")
					args = append(args, pid)
				}
			}
		}
		if len(whereParts) > 0 {
			vquery := fmt.Sprintf(`SELECT v.poem_id, v.vorder, v.position,
				CASE WHEN v2.text IS NOT NULL THEN v.text || ' / ' || v2.text ELSE v.text END AS full_text,
				po.title, p.name AS poet_name, p.id AS poet_id, p.century_id, c.text AS cat_title
				FROM verse v
				LEFT JOIN verse v2 ON v2.poem_id = v.poem_id AND v2.vorder = v.vorder + 1 AND ((v.position = 0 AND v2.position = 1) OR (v.position = 2 AND v2.position = 3))
				JOIN poem po ON po.id = v.poem_id
				JOIN cat c ON c.id = po.cat_id
				JOIN poet p ON p.id = c.poet_id
				WHERE %s ORDER BY v.poem_id, v.vorder, v.position`, strings.Join(whereParts, " OR "))
			rows, err := db.Query(vquery, args...)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var item SearchVerseResult
					if err := rows.Scan(&item.PoemID, &item.VOrder, &item.Position, &item.Text, &item.Title, &item.PoetName, &item.PoetID, &item.CenturyID, &item.CatTitle); err == nil {
						resp.Verses = append(resp.Verses, item)
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ==========================================================================
// MASTERPIECE COUPLET INDEX (Daily Verse, Mood Discovery, Mushaira & Rhyme)
// ==========================================================================

type CoupletEntry struct {
	PoemID       int64  `json:"poem_id"`
	VOrder       int64  `json:"vorder"`
	Hemistich1   string `json:"hemistich_1"`
	Hemistich2   string `json:"hemistich_2"`
	PoemTitle    string `json:"poem_title"`
	CatTitle     string `json:"cat_title"`
	PoetID       int64  `json:"poet_id"`
	PoetName     string `json:"poet_name"`
	StartLetter  string `json:"start_letter"`
	EndLetter    string `json:"end_letter"`
	RhymeWord    string `json:"rhyme_word,omitempty"`
	rhymeLetters []rune
}

var (
	coupletPool      []CoupletEntry
	coupletsByLetter map[string][]int
	coupletIndexMu   sync.RWMutex
)

func extractPersianLetters(s string) []rune {
	var out []rune
	for _, r := range s {
		switch r {
		case '\u0622', '\u0623', '\u0625', '\u0671', '\u0627':
			out = append(out, 'ا')
		case '\u064A', '\u0649', '\u0626', '\u06CC':
			out = append(out, 'ی')
		case '\u0643', '\u06A9':
			out = append(out, 'ک')
		case '\u0629', '\u0647', '\u06BE', '\u06D5':
			out = append(out, 'ه')
		case '\u0624', '\u0648':
			out = append(out, 'و')
		case '\u067E', '\u0686', '\u0698', '\u06AF':
			out = append(out, r)
		default:
			if (r >= '\u0628' && r <= '\u063A') || (r >= '\u0641' && r <= '\u0648') {
				out = append(out, r)
			}
		}
	}
	return out
}

func cleanWordsForRhyme(s string) []string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u200C', '\u200D', '\u0640', '\u064B', '\u064C', '\u064D', '\u064E', '\u064F', '\u0650', '\u0651', '\u0652', '\u0653', '\u0654', '\u0655', '\u0670':
			continue
		case '\u064A', '\u0649', '\u0626':
			b.WriteRune('ی')
		case '\u0643':
			b.WriteRune('ک')
		case '\u0623', '\u0625', '\u0671':
			b.WriteRune('ا')
		case '\u0629':
			b.WriteRune('ه')
		default:
			if unicode.IsSpace(r) {
				b.WriteRune(' ')
			} else if unicode.IsLetter(r) {
				b.WriteRune(r)
			} else {
				b.WriteRune(' ')
			}
		}
	}
	return strings.Fields(b.String())
}

var commonRadifs = map[string]bool{
	"را": true, "است": true, "بود": true, "شد": true, "باد": true,
	"کرد": true, "گشت": true, "نیست": true, "دارد": true, "تو": true,
	"من": true, "ما": true, "او": true, "ای": true,
}

func pickRhymeWord(h1, h2 string) string {
	w1 := cleanWordsForRhyme(h1)
	w2 := cleanWordsForRhyme(h2)
	if len(w2) == 0 {
		return ""
	}
	idx2 := len(w2) - 1
	idx1 := len(w1) - 1
	// Strip shared trailing radif in rhyming opening couplets (e.g. "... خدا را" / "... آشنا را")
	steps := 0
	for idx1 > 0 && idx2 > 0 && w1[idx1] == w2[idx2] && steps < 3 {
		idx1--
		idx2--
		steps++
	}
	if steps == 0 && idx2 > 0 && commonRadifs[w2[idx2]] {
		idx2--
	}
	return w2[idx2]
}

func loadCoupletIndex() ([]CoupletEntry, map[string][]int, error) {
	coupletIndexMu.RLock()
	if len(coupletPool) > 0 {
		defer coupletIndexMu.RUnlock()
		return coupletPool, coupletsByLetter, nil
	}
	coupletIndexMu.RUnlock()

	coupletIndexMu.Lock()
	defer coupletIndexMu.Unlock()
	if len(coupletPool) > 0 {
		return coupletPool, coupletsByLetter, nil
	}

	// Load ~12,000 iconic opening & second couplets across major classical & modern masters
	rows, err := db.Query(`
		SELECT v1.poem_id, v1.vorder, v1.text, v2.text, po.title, c.text, p.id, p.name
		FROM cat c
		JOIN poem po ON po.cat_id = c.id
		JOIN verse v1 ON v1.poem_id = po.id AND v1.position = 0 AND v1.vorder IN (1, 3)
		JOIN verse v2 ON v2.poem_id = po.id AND v2.position = 1 AND v2.vorder = v1.vorder + 1
		JOIN poet p ON p.id = c.poet_id
		WHERE c.poet_id IN (2, 5, 7, 3, 6, 9, 12, 20, 22, 24, 25, 26, 27, 28, 29, 32, 33, 35, 43)
		LIMIT 12000`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	pool := make([]CoupletEntry, 0, 12000)
	byLetter := make(map[string][]int)

	for rows.Next() {
		var e CoupletEntry
		if err := rows.Scan(&e.PoemID, &e.VOrder, &e.Hemistich1, &e.Hemistich2, &e.PoemTitle, &e.CatTitle, &e.PoetID, &e.PoetName); err != nil {
			continue
		}
		l1 := extractPersianLetters(e.Hemistich1)
		l2 := extractPersianLetters(e.Hemistich2)
		if len(l1) < 6 || len(l2) < 6 {
			continue
		}
		e.StartLetter = string(l1[0])
		e.EndLetter = string(l2[len(l2)-1])
		e.RhymeWord = pickRhymeWord(e.Hemistich1, e.Hemistich2)
		e.rhymeLetters = extractPersianLetters(e.RhymeWord)

		idx := len(pool)
		pool = append(pool, e)
		byLetter[e.StartLetter] = append(byLetter[e.StartLetter], idx)
	}

	coupletPool = pool
	coupletsByLetter = byLetter
	return coupletPool, coupletsByLetter, nil
}

// 13. Daily Masterpiece Couplet (بیت امروز + بیت تصادفی)
func handleDaily(w http.ResponseWriter, r *http.Request) {
	pool, _, err := loadCoupletIndex()
	if err != nil || len(pool) == 0 {
		http.Error(w, "Couplet index unavailable", http.StatusServiceUnavailable)
		return
	}

	isRandom := r.URL.Query().Get("random") == "1"
	if isRandom {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		picked := pool[rand.Intn(len(pool))]
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"date":    "random",
			"couplet": picked,
		})
		return
	}

	// Deterministic daily selection on Iran Standard Time (UTC+03:30)
	iranNow := time.Now().UTC().Add(3*time.Hour + 30*time.Minute)
	dateKey := iranNow.Format("2006-01-02")
	w.Header().Set("Cache-Control", "public, max-age=900")

	h := fnv.New32a()
	_, _ = h.Write([]byte("qaf-daily-masterpiece-" + dateKey))
	picked := pool[int(h.Sum32())%len(pool)]

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"date":    dateKey,
		"couplet": picked,
	})
}

var discoverMoodKeywords = map[string][]string{
	"hope":   {"امید", "سحر", "صبح", "خورشید", "نور", "بهار", "روشن", "شکوفه"},
	"love":   {"عشق", "دلبر", "جانان", "معشوق", "وصال", "زلف", "مهر", "یار"},
	"calm":   {"آرام", "صبر", "قرار", "سکوت", "نسیم", "شکیب", "آسود", "خوش"},
	"wisdom": {"خرد", "عمر", "جهان", "روزگار", "پند", "دانش", "نیکی", "درخت"},
	"joy":    {"شادی", "طرب", "مژده", "خندان", "باده", "ساقی", "گلستان", "عیش"},
	"mystic": {"جان", "حق", "راز", "معرفت", "آیینه", "سرمست", "آسمان", "پرواز"},
}

// 14. Mood / Vibe Poetry Discovery (شعرِ حالِ تو)
func handleDiscover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	pool, _, err := loadCoupletIndex()
	if err != nil || len(pool) == 0 {
		http.Error(w, "Couplet index unavailable", http.StatusServiceUnavailable)
		return
	}

	mood := r.URL.Query().Get("mood")
	keywords, ok := discoverMoodKeywords[mood]
	if !ok {
		mood = "hope"
		keywords = discoverMoodKeywords["hope"]
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 20 {
		limit = 6
	}

	var matched []int
	for i := range pool {
		txt := pool[i].Hemistich1 + " " + pool[i].Hemistich2
		for _, kw := range keywords {
			if strings.Contains(txt, kw) {
				matched = append(matched, i)
				break
			}
		}
	}
	if len(matched) == 0 {
		for i := range pool {
			matched = append(matched, i)
		}
	}

	// Sample up to limit diverse couplets
	perm := rand.Perm(len(matched))
	out := make([]CoupletEntry, 0, limit)
	usedPoets := make(map[int64]int)
	for _, pIdx := range perm {
		c := pool[matched[pIdx]]
		if usedPoets[c.PoetID] >= 2 && len(matched) > limit*3 {
			continue
		}
		usedPoets[c.PoetID]++
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mood":     mood,
		"couplets": out,
	})
}

// 15. Interactive Mushaira Engine & Verse Finder (مشاعره هوشمند)
func handleMushaira(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	pool, byLetter, err := loadCoupletIndex()
	if err != nil || len(pool) == 0 {
		http.Error(w, "Couplet index unavailable", http.StatusServiceUnavailable)
		return
	}

	rawLetter := strings.TrimSpace(r.URL.Query().Get("letter"))
	userVerse := strings.TrimSpace(r.URL.Query().Get("user_verse"))
	excludeRaw := r.URL.Query().Get("exclude")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 30 {
		limit = 6
	}

	excluded := make(map[int64]bool)
	if excludeRaw != "" {
		for _, part := range strings.Split(excludeRaw, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
				excluded[id] = true
			}
		}
	}

	var userStart, userEnd string
	verifiedInArchive := false
	if userVerse != "" {
		uLetters := extractPersianLetters(userVerse)
		if len(uLetters) < 4 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "متن بیت بسیار کوتاه است."})
			return
		}
		userStart = string(uLetters[0])
		userEnd = string(uLetters[len(uLetters)-1])
		// Check if a phrase from user's verse appears in verse_fts
		normUser := normalizePersian(userVerse)
		words := strings.Fields(normUser)
		if len(words) > 4 {
			words = words[:4]
		}
		if fts := buildFTSMatchExpr(strings.Join(words, " ")); fts != "" {
			var hit int
			if err := db.QueryRow(`SELECT 1 FROM verse_fts WHERE verse_fts MATCH ? LIMIT 1`, fts).Scan(&hit); err == nil && hit == 1 {
				verifiedInArchive = true
			}
		}
		if rawLetter == "" {
			rawLetter = userEnd
		}
	}

	targetLetter := ""
	if rawLetter != "" {
		rLetters := extractPersianLetters(rawLetter)
		if len(rLetters) > 0 {
			targetLetter = string(rLetters[0])
		}
	}
	if targetLetter == "" || len(byLetter[targetLetter]) == 0 {
		commonStarts := []string{"د", "ب", "م", "س", "ا", "چ", "گ", "ن", "ز", "ت", "ه", "ی"}
		for _, ch := range commonStarts {
			if len(byLetter[ch]) > 0 && targetLetter == "" {
				targetLetter = commonStarts[rand.Intn(len(commonStarts))]
				if len(byLetter[targetLetter]) == 0 {
					targetLetter = ch
				}
				break
			}
		}
	}

	indices := byLetter[targetLetter]
	suggestions := make([]CoupletEntry, 0, limit)
	if len(indices) > 0 {
		perm := rand.Perm(len(indices))
		for _, pIdx := range perm {
			entry := pool[indices[pIdx]]
			if excluded[entry.PoemID] {
				continue
			}
			suggestions = append(suggestions, entry)
			if len(suggestions) >= limit {
				break
			}
		}
		if len(suggestions) == 0 {
			suggestions = append(suggestions, pool[indices[rand.Intn(len(indices))]])
		}
	}

	var reply *CoupletEntry
	if len(suggestions) > 0 {
		c := suggestions[0]
		reply = &c
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"letter":              targetLetter,
		"user_start_letter":   userStart,
		"user_end_letter":     userEnd,
		"verified_in_archive": verifiedInArchive,
		"reply":               reply,
		"suggestions":         suggestions,
	})
}

// 16. Classical Rhyme & Radif Finder (قافیه‌یاب)
func handleRhyme(w http.ResponseWriter, r *http.Request) {
	rawWord := strings.TrimSpace(r.URL.Query().Get("word"))
	targetLetters := extractPersianLetters(rawWord)
	if len(targetLetters) < 2 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"word":     rawWord,
			"rhymes":   []string{},
			"couplets": []CoupletEntry{},
		})
		return
	}

	pool, _, err := loadCoupletIndex()
	if err != nil || len(pool) == 0 {
		http.Error(w, "Couplet index unavailable", http.StatusServiceUnavailable)
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	targetClean := string(targetLetters)
	type scoredRhyme struct {
		idx   int
		score int
	}
	var hits []scoredRhyme

	for i := range pool {
		rl := pool[i].rhymeLetters
		if len(rl) < 2 {
			continue
		}
		candClean := string(rl)
		if candClean == targetClean {
			continue // skip identical word (radif-only repetition)
		}
		// Count matching trailing letters
		matchedSuffix := 0
		for a, b := len(targetLetters)-1, len(rl)-1; a >= 0 && b >= 0; a, b = a-1, b-1 {
			if targetLetters[a] == rl[b] {
				matchedSuffix++
			} else {
				break
			}
		}
		if matchedSuffix >= 2 {
			score := matchedSuffix * 10
			if len(rl) == len(targetLetters) {
				score += 3 // similar prosodic weight bonus
			}
			hits = append(hits, scoredRhyme{idx: i, score: score})
		}
	}

	// Sort descending by rhyme suffix quality
	for i := 0; i < len(hits)-1 && i < limit*3; i++ {
		best := i
		for j := i + 1; j < len(hits); j++ {
			if hits[j].score > hits[best].score {
				best = j
			}
		}
		if best != i {
			hits[i], hits[best] = hits[best], hits[i]
		}
	}

	rhymes := make([]string, 0, 24)
	seenWords := make(map[string]bool)
	couplets := make([]CoupletEntry, 0, limit)
	seenPoems := make(map[int64]bool)

	for _, h := range hits {
		e := pool[h.idx]
		wNorm := string(e.rhymeLetters)
		if e.RhymeWord != "" && !seenWords[wNorm] && len(rhymes) < 24 {
			seenWords[wNorm] = true
			rhymes = append(rhymes, e.RhymeWord)
		}
		if !seenPoems[e.PoemID] && len(couplets) < limit {
			seenPoems[e.PoemID] = true
			couplets = append(couplets, e)
		}
		if len(rhymes) >= 24 && len(couplets) >= limit {
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"word":     rawWord,
		"rhymes":   rhymes,
		"couplets": couplets,
	})
}

// 17. Offline Poet Pack (بسته مطالعه آفلاین سخنور)
func handlePoetPack(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	poetID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || poetID <= 0 {
		http.Error(w, "Invalid poet ID", http.StatusBadRequest)
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 80 {
		limit = 40
	}

	var p Poet
	err = db.QueryRow(`SELECT id, name, cat_id, description, century_id, birth_year, death_year, birthplace, deathplace 
		FROM poet WHERE id = ? LIMIT 1`, poetID).
		Scan(&p.ID, &p.Name, &p.CatID, &p.Description, &p.CenturyID, &p.BirthYear, &p.DeathYear, &p.Birthplace, &p.Deathplace)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Poet not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	prows, err := db.Query(`
		SELECT po.id, po.title, po.cat_id, c.text
		FROM poem po
		JOIN cat c ON c.id = po.cat_id
		WHERE c.poet_id = ?
		ORDER BY po.id ASC
		LIMIT ?`, poetID, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer prows.Close()

	var poemsMeta []PoemDetail
	var poemIDs []int64
	var args []interface{}
	for prows.Next() {
		var pm PoemDetail
		if err := prows.Scan(&pm.PoemID, &pm.Title, &pm.CatID, &pm.CatTitle); err == nil {
			pm.PoetID = p.ID
			pm.PoetName = p.Name
			pm.CenturyID = p.CenturyID
			pm.Birthplace = p.Birthplace
			poemsMeta = append(poemsMeta, pm)
			poemIDs = append(poemIDs, pm.PoemID)
			args = append(args, pm.PoemID)
		}
	}

	versesByPoem := make(map[int64][]VerseItem)
	if len(args) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(args)), ",")
		vrows, verr := db.Query(fmt.Sprintf(`SELECT poem_id, vorder, position, text FROM verse WHERE poem_id IN (%s) ORDER BY poem_id, vorder, position`, placeholders), args...)
		if verr == nil {
			defer vrows.Close()
			for vrows.Next() {
				var v VerseItem
				if err := vrows.Scan(&v.PoemID, &v.VOrder, &v.Position, &v.Text); err == nil {
					versesByPoem[v.PoemID] = append(versesByPoem[v.PoemID], v)
				}
			}
		}
	}

	pack := make([]PoemDetailResponse, 0, len(poemsMeta))
	for i := range poemsMeta {
		pm := poemsMeta[i]
		pack = append(pack, PoemDetailResponse{
			Poem:    &pm,
			Verses:  groupVerseItems(versesByPoem[pm.PoemID]),
			PoemIDs: poemIDs,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"poet":  p,
		"count": len(pack),
		"poems": pack,
	})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "qaf.db"
	}

	log.Printf("==> Starting Qaf API Server on port %s (serving: %s in read-only mode)", port, dbPath)

	// SQLite connection string with read-only and performance pragmas
	dsn := fmt.Sprintf("file:%s?mode=ro&cache=shared&_journal_mode=WAL&_synchronous=NORMAL", dbPath)
	var err error
	db, err = sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("Failed to open SQLite database: %v", err)
	}
	defer db.Close()

	// High concurrency connection pool settings
	maxConns := runtime.NumCPU() * 4
	if maxConns < 8 {
		maxConns = 8
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns / 2)
	db.SetConnMaxLifetime(time.Hour)

	// Verify database connection
	if err := db.Ping(); err != nil {
		log.Fatalf("Database ping failed: %v", err)
	}
	log.Printf("--> Database connected successfully (%d max worker connections)", maxConns)

	// Preload filter options & masterpiece couplet index in background
	go func() {
		if _, err := loadFilterOptions(); err != nil {
			log.Printf("Warning: failed to preload filter options: %v", err)
		} else {
			log.Printf("--> Filter options cached in memory")
		}
		if pool, _, err := loadCoupletIndex(); err != nil {
			log.Printf("Warning: failed to preload couplet index: %v", err)
		} else {
			log.Printf("--> Masterpiece couplet index cached in memory (%d couplets)", len(pool))
		}
	}()

	mux := http.NewServeMux()

	// Register API endpoints
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /api/home", handleHome)
	mux.HandleFunc("GET /api/filter-options", handleFilterOptions)
	mux.HandleFunc("GET /api/poets", handlePoets)
	mux.HandleFunc("GET /api/poets/{id}", handlePoetDetail)
	mux.HandleFunc("GET /api/poets/{id}/pack", handlePoetPack)
	mux.HandleFunc("GET /api/works", handleWorks)
	mux.HandleFunc("GET /api/works/{catId}", handleWorkDetail)
	mux.HandleFunc("GET /api/works/{catId}/poems", handleWorkPoems)
	mux.HandleFunc("GET /api/poems/{id}", handlePoemDetail)
	mux.HandleFunc("GET /api/search", handleSearch)
	mux.HandleFunc("POST /api/bookmarks", handleBookmarks)
	mux.HandleFunc("GET /api/faal", handleFaal)
	mux.HandleFunc("GET /api/daily", handleDaily)
	mux.HandleFunc("GET /api/discover", handleDiscover)
	mux.HandleFunc("GET /api/mushaira", handleMushaira)
	mux.HandleFunc("GET /api/rhyme", handleRhyme)

	// Wrap mux with CORS, Gzip, and static caching middleware
	handler := corsAndGzipMiddleware(mux)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("--> Qaf HTTP API is listening on http://0.0.0.0:%s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server stopped with error: %v", err)
	}
}
