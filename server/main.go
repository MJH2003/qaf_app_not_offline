package main

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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
		case '\u064A', '\u0649': // Arabic Yeh, Alef Maksura -> Persian Yeh
			b.WriteRune('\u06CC')
		case '\u0643': // Arabic Kaf -> Persian Kaf
			b.WriteRune('\u06A9')
		case '\u0622', '\u0623', '\u0625': // Alef with madd/hamza -> Alef
			b.WriteRune('\u0627')
		case '\u0640': // Tatweel
			continue
		case '\u064B', '\u064C', '\u064D', '\u064E', '\u064F', '\u0650', '\u0651', '\u0652', '\u0670': // Diacritics
			continue
		case '\u200C': // ZWNJ / half-space -> space
			b.WriteRune(' ')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.TrimSpace(b.String())
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Origin, X-Requested-With")
		w.Header().Set("Access-Control-Max-Age", "86400")

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
		where = append(where, "(name LIKE ? OR birthplace LIKE ?)")
		args = append(args, "%"+q+"%", "%"+q+"%")
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
		where = append(where, "(birthplace LIKE ? OR deathplace LIKE ?)")
		args = append(args, "%"+city+"%", "%"+city+"%")
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
		where = append(where, "(c.text LIKE ? OR p.name LIKE ?)")
		args = append(args, "%"+q+"%", "%"+q+"%")
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
		q := normalizePersian(r.URL.Query().Get("q"))
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
		where = append(where, "po.cat_id = ?")
		args = append(args, catID)

		if q != "" {
			where = append(where, "po.title LIKE ?")
			args = append(args, "%"+q+"%")
		}

		pquery := fmt.Sprintf(`SELECT po.id AS poem_id, po.title, c.text AS cat_title, p.name AS poet_name,
			(SELECT GROUP_CONCAT(v.text, ' ') FROM verse v WHERE v.poem_id=po.id AND v.vorder=(SELECT MIN(v2.vorder) FROM verse v2 WHERE v2.poem_id=po.id)) AS preview
			FROM poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id
			WHERE %s ORDER BY po.id LIMIT %d OFFSET %d`, strings.Join(where, " AND "), limit, offset)

		prows, err := db.Query(pquery, args...)
		if err == nil {
			defer prows.Close()
			for prows.Next() {
				var fp FlatPoem
				if err := prows.Scan(&fp.PoemID, &fp.Title, &fp.CatTitle, &fp.PoetName, &fp.Preview); err == nil {
					poems = append(poems, fp)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, WorkDetailResponse{
		Work:     &work,
		Children: children,
		Poems:    poems,
	})
}

// 8. Work Poems Direct Pagination
func handleWorkPoems(w http.ResponseWriter, r *http.Request) {
	catIDStr := r.PathValue("catId")
	catID, err := strconv.ParseInt(catIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid catId", http.StatusBadRequest)
		return
	}

	q := normalizePersian(r.URL.Query().Get("q"))
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
	where = append(where, "po.cat_id = ?")
	args = append(args, catID)

	if q != "" {
		where = append(where, "po.title LIKE ?")
		args = append(args, "%"+q+"%")
	}

	pquery := fmt.Sprintf(`SELECT po.id AS poem_id, po.title, c.text AS cat_title, p.name AS poet_name,
		(SELECT GROUP_CONCAT(v.text, ' ') FROM verse v WHERE v.poem_id=po.id AND v.vorder=(SELECT MIN(v2.vorder) FROM verse v2 WHERE v2.poem_id=po.id)) AS preview
		FROM poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id
		WHERE %s ORDER BY po.id LIMIT %d OFFSET %d`, strings.Join(where, " AND "), limit, offset)

	prows, err := db.Query(pquery, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer prows.Close()

	poems := make([]FlatPoem, 0)
	for prows.Next() {
		var fp FlatPoem
		if err := prows.Scan(&fp.PoemID, &fp.Title, &fp.CatTitle, &fp.PoetName, &fp.Preview); err == nil {
			poems = append(poems, fp)
		}
	}
	writeJSON(w, http.StatusOK, poems)
}

// 9. Poem Reader
func handlePoemDetail(w http.ResponseWriter, r *http.Request) {
	poemIDStr := r.PathValue("id")
	poemID, err := strconv.ParseInt(poemIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid poem ID", http.StatusBadRequest)
		return
	}

	var poem PoemDetail
	err = db.QueryRow(`
		SELECT po.id AS poem_id, po.title, po.cat_id, c.text AS cat_title, p.id AS poet_id, p.name AS poet_name, p.century_id, p.birthplace
		FROM poem po JOIN cat c ON c.id=po.cat_id JOIN poet p ON p.id=c.poet_id 
		WHERE po.id = ? LIMIT 1`, poemID).
		Scan(&poem.PoemID, &poem.Title, &poem.CatID, &poem.CatTitle, &poem.PoetID, &poem.PoetName, &poem.CenturyID, &poem.Birthplace)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Poem not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Sibling poem IDs for prev/next navigation
	srows, err := db.Query(`SELECT id FROM poem WHERE cat_id = ? ORDER BY id`, poem.CatID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer vrows.Close()

	groupsMap := make(map[int64][]VerseItem)
	var groupOrders []int64
	for vrows.Next() {
		var v VerseItem
		if err := vrows.Scan(&v.PoemID, &v.VOrder, &v.Position, &v.Text); err == nil {
			if _, exists := groupsMap[v.VOrder]; !exists {
				groupOrders = append(groupOrders, v.VOrder)
			}
			groupsMap[v.VOrder] = append(groupsMap[v.VOrder], v)
		}
	}

	verses := make([]VerseGroup, 0, len(groupOrders))
	for _, order := range groupOrders {
		verses = append(verses, VerseGroup{
			Order: order,
			Items: groupsMap[order],
		})
	}

	writeJSON(w, http.StatusOK, PoemDetailResponse{
		Poem:    &poem,
		Verses:  verses,
		PoemIDs: poemIDs,
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

	// Helper for FTS match expression
	buildFTSQuery := func(text string) string {
		words := strings.Fields(text)
		if len(words) == 0 {
			return ""
		}
		var parts []string
		for _, w := range words {
			w = strings.ReplaceAll(w, `"`, "")
			if w != "" {
				parts = append(parts, fmt.Sprintf(`"%s"*`, w))
			}
		}
		return strings.Join(parts, " AND ")
	}

	ftsExpr := buildFTSQuery(q)

	// A. Search Poets
	if (scope == "all" || scope == "poets") && (q != "" || century != "") {
		var where []string
		var args []interface{}
		if q != "" {
			where = append(where, "(name LIKE ? OR birthplace LIKE ?)")
			args = append(args, "%"+q+"%", "%"+q+"%")
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
			where = append(where, "(c.text LIKE ? OR p.name LIKE ?)")
			args = append(args, "%"+q+"%", "%"+q+"%")
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
			whereFallback := []string{"po.title LIKE ?"}
			argsFallback := []interface{}{"%" + q + "%"}
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

	// D. Search Verses (using verse_fts for sub-5ms search)
	if (scope == "all" || scope == "verses") && len(q) >= 2 {
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
				SELECT v.poem_id, v.vorder, v.position, v.text, po.title, p.name AS poet_name, p.id AS poet_id, p.century_id, c.text AS cat_title
				FROM verse_fts f
				JOIN verse v ON v.rowid = f.rowid
				JOIN poem po ON po.id = v.poem_id
				JOIN cat c ON c.id = po.cat_id
				JOIN poet p ON p.id = c.poet_id
				WHERE %s LIMIT %d`, strings.Join(where, " AND "), verseLimit)

			rows, err := db.Query(vquery, args...)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var item SearchVerseResult
					if err := rows.Scan(&item.PoemID, &item.VOrder, &item.Position, &item.Text, &item.Title, &item.PoetName, &item.PoetID, &item.CenturyID, &item.CatTitle); err == nil {
						res.Verses = append(res.Verses, item)
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, res)
}

// 11. Batch Bookmarks Resolver
func handleBookmarks(w http.ResponseWriter, r *http.Request) {
	var req BookmarkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid bookmark request body", http.StatusBadRequest)
		return
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

	// 4. Resolve Verses (format "poem_id:vorder")
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
					whereParts = append(whereParts, "(v.poem_id = ?)")
					args = append(args, pid)
				}
			}
		}
		if len(whereParts) > 0 {
			vquery := fmt.Sprintf(`SELECT v.poem_id, v.vorder, v.position, v.text, po.title, p.name AS poet_name, p.id AS poet_id, p.century_id, c.text AS cat_title
				FROM verse v
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

	// Preload filter options cache in background
	go func() {
		if _, err := loadFilterOptions(); err != nil {
			log.Printf("Warning: failed to preload filter options: %v", err)
		} else {
			log.Printf("--> Filter options cached in memory")
		}
	}()

	mux := http.NewServeMux()

	// Register API endpoints
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /api/home", handleHome)
	mux.HandleFunc("GET /api/filter-options", handleFilterOptions)
	mux.HandleFunc("GET /api/poets", handlePoets)
	mux.HandleFunc("GET /api/poets/{id}", handlePoetDetail)
	mux.HandleFunc("GET /api/works", handleWorks)
	mux.HandleFunc("GET /api/works/{catId}", handleWorkDetail)
	mux.HandleFunc("GET /api/works/{catId}/poems", handleWorkPoems)
	mux.HandleFunc("GET /api/poems/{id}", handlePoemDetail)
	mux.HandleFunc("GET /api/search", handleSearch)
	mux.HandleFunc("POST /api/bookmarks", handleBookmarks)

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
