//go:build ignore

package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := "qaf.db"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	} else if p := os.Getenv("DB_PATH"); p != "" {
		dbPath = p
	}

	fmt.Printf("==> Starting Qaf SQLite Database Optimization on: %s\n", dbPath)
	startTotal := time.Now()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Configure low-memory pragmas safe for small VPS instances (512MB - 1GB RAM)
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA cache_size = -32000;", // 32 MB cache (safe for small RAM)
		"PRAGMA temp_store = FILE;",   // Use disk for temp index sorting to avoid OOM
		"PRAGMA mmap_size = 33554432;", // 32 MB memory-mapped I/O
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			log.Printf("Warning setting pragma %s: %v", p, err)
		}
	}

	// 1. Normalize characters in core tables (ي -> ی, ى -> ی, ك -> ک)
	fmt.Println("--> Normalizing Arabic characters in poet, cat, poem, and verse...")
	t0 := time.Now()
	normalizations := []struct {
		table string
		col   string
	}{
		{"poet", "name"},
		{"poet", "description"},
		{"cat", "text"},
		{"poem", "title"},
		{"verse", "text"},
	}
	for _, n := range normalizations {
		query := fmt.Sprintf(`UPDATE [%s] SET [%s] = REPLACE(REPLACE(REPLACE([%s], CHAR(1610), CHAR(1740)), CHAR(1609), CHAR(1740)), CHAR(1603), CHAR(1705))
			WHERE INSTR([%s], CHAR(1610)) > 0 OR INSTR([%s], CHAR(1609)) > 0 OR INSTR([%s], CHAR(1603)) > 0`,
			n.table, n.col, n.col, n.col, n.col, n.col)
		res, err := db.Exec(query)
		if err != nil {
			log.Printf("Warning normalizing %s.%s: %v", n.table, n.col, err)
		} else {
			affected, _ := res.RowsAffected()
			if affected > 0 {
				fmt.Printf("    Normalized %d rows in %s.%s\n", affected, n.table, n.col)
			}
		}
	}
	fmt.Printf("    Character normalization completed in %v\n", time.Since(t0))

	// 2. Create optimized B-tree indexes
	fmt.Println("--> Creating auxiliary B-tree indexes...")
	t0 = time.Now()
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_cat_poet_id ON cat(poet_id);",
		"CREATE INDEX IF NOT EXISTS idx_cat_parent_id ON cat(parent_id);",
		"CREATE INDEX IF NOT EXISTS idx_poem_cat_id ON poem(cat_id);",
		"CREATE INDEX IF NOT EXISTS idx_poet_century ON poet(century_id);",
		"CREATE INDEX IF NOT EXISTS idx_verse_poem_id ON verse(poem_id);",
		"CREATE INDEX IF NOT EXISTS idx_verse_poem_vorder ON verse(poem_id, vorder, position);",
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			log.Printf("Warning creating index %s: %v", idx, err)
		}
	}
	fmt.Printf("    B-tree indexes verified/created in %v\n", time.Since(t0))

	// 3. Build FTS5 virtual table for verses
	fmt.Println("--> Building verse_fts FTS5 index (indexing ~3 million verses)...")
	t0 = time.Now()
	_, err = db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS verse_fts USING fts5(
		text,
		content='verse',
		content_rowid='rowid',
		tokenize='unicode61 remove_diacritics 2'
	);`)
	if err != nil {
		log.Fatalf("Failed to create verse_fts: %v", err)
	}

	fmt.Println("    Rebuilding verse_fts index...")
	if _, err := db.Exec("INSERT INTO verse_fts(verse_fts) VALUES('rebuild');"); err != nil {
		log.Fatalf("Failed to rebuild verse_fts: %v", err)
	}
	fmt.Printf("    verse_fts built successfully in %v\n", time.Since(t0))

	// 4. Build FTS5 virtual table for poems
	fmt.Println("--> Building poem_fts FTS5 index (indexing ~133k poems)...")
	t0 = time.Now()
	_, err = db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS poem_fts USING fts5(
		title,
		content='poem',
		content_rowid='id',
		tokenize='unicode61 remove_diacritics 2'
	);`)
	if err != nil {
		log.Fatalf("Failed to create poem_fts: %v", err)
	}

	fmt.Println("    Rebuilding poem_fts index...")
	if _, err := db.Exec("INSERT INTO poem_fts(poem_fts) VALUES('rebuild');"); err != nil {
		log.Fatalf("Failed to rebuild poem_fts: %v", err)
	}
	fmt.Printf("    poem_fts built successfully in %v\n", time.Since(t0))

	// 5. Run ANALYZE for query planner statistics
	fmt.Println("--> Running ANALYZE for query optimizer statistics...")
	t0 = time.Now()
	if _, err := db.Exec("ANALYZE;"); err != nil {
		log.Printf("Warning running ANALYZE: %v", err)
	}
	fmt.Printf("    ANALYZE completed in %v\n", time.Since(t0))

	// 6. Test FTS query speed
	fmt.Println("--> Running benchmark test queries...")
	testWords := []string{"ساقی", "عشق", "سعدی", "شیراز", "پروانه"}
	for _, word := range testWords {
		tQuery := time.Now()
		rows, err := db.Query(`
			SELECT v.poem_id, v.vorder, v.position, v.text
			FROM verse_fts f
			JOIN verse v ON v.rowid = f.rowid
			WHERE verse_fts MATCH ?
			LIMIT 10`, word)
		if err != nil {
			log.Fatalf("Query test failed for word '%s': %v", word, err)
		}
		count := 0
		for rows.Next() {
			count++
		}
		rows.Close()
		dur := time.Since(tQuery)
		fmt.Printf("    Search '%s': %d results returned in %v\n", word, count, dur)
	}

	fmt.Printf("\n==> Optimization complete! Total time: %v\n", time.Since(startTotal))
}
