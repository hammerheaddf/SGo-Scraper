package main

import (
	"archive/zip"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func checkAndCreateDir(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		os.MkdirAll(path, os.ModePerm)
	}
}

// saveImage is used for proper album sets, blog images, and group threads.
func saveImage(imageURL string, output string) (int64, error) {
	client := newAuthedClient(imageURL)
	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Referer", "https://www.suicidegirls.com/")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("image request failed: %s", resp.Status)
	}

	if ct := resp.Header.Get("Content-Type"); strings.Contains(strings.ToLower(ct), "text/html") {
		return 0, fmt.Errorf("image URL returned HTML (likely ad redirect): %s", ct)
	}

	img, err := os.Create(output)
	if err != nil {
		return 0, err
	}
	defer img.Close()

	n, err := io.Copy(img, resp.Body)
	if err != nil {
		return n, err
	}

	if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
		if t, err := http.ParseTime(lastMod); err == nil {
			os.Chtimes(output, t, t)
		}
	}

	return n, nil
}

func ZipFiles(filename string, files []string) error {
	newfile, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer newfile.Close()

	zipWriter := zip.NewWriter(newfile)
	defer zipWriter.Close()

	for _, file := range files {
		if file == "" {
			continue
		}

		zipfile, err := os.Open(file)
		if err != nil {
			return err
		}

		info, err := zipfile.Stat()
		if err != nil {
			zipfile.Close()
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			zipfile.Close()
			return err
		}

		header.Name = filepath.Base(file)
		header.Method = zip.Deflate

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			zipfile.Close()
			return err
		}

		if _, err = io.Copy(writer, zipfile); err != nil {
			zipfile.Close()
			return err
		}

		zipfile.Close()
	}

	return nil
}

func getDBDir() string {
	exePath, err := os.Executable()
	var baseDir string
	if err == nil {
		baseDir = filepath.Dir(exePath)
	} else {
		baseDir = "." // Fallback to current working directory
	}
	dbDir := filepath.Join(baseDir, "modelsdb")
	checkAndCreateDir(dbDir)
	return dbDir
}

func getModelDB(modelName string) (*sql.DB, error) {
	dbDir := getDBDir()
	safeModel := sanitizeName(modelName)
	dbPath := filepath.Join(dbDir, safeModel+".db")
	
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	
	schema := `
	CREATE TABLE IF NOT EXISTS downloads (
		type TEXT,
		item_id TEXT,
		title TEXT,
		downloaded_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (type, item_id)
	);`
	_, err = db.Exec(schema)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func getGroupDB() (*sql.DB, error) {
	dbDir := getDBDir()
	dbPath := filepath.Join(dbDir, "groups.db")
	
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	
	schema := `
	CREATE TABLE IF NOT EXISTS downloads (
		type TEXT,
		item_id TEXT,
		title TEXT,
		downloaded_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (type, item_id)
	);`
	_, err = db.Exec(schema)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func isDownloaded(db *sql.DB, itemType string, itemID string) bool {
	if db == nil {
		return false
	}
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM downloads WHERE type = ? AND item_id = ?)", itemType, itemID).Scan(&exists)
	if err != nil {
		return false
	}
	return exists
}

func markDownloaded(db *sql.DB, itemType string, itemID string, title string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("INSERT OR REPLACE INTO downloads (type, item_id, title) VALUES (?, ?, ?)", itemType, itemID, title)
	return err
}

func formatIndex(curr, total int) string {
	if total <= 0 {
		return ""
	}
	width := len(strconv.Itoa(total))
	if width < 2 {
		width = 2
	}
	return fmt.Sprintf("[%0*d/%0*d] ", width, curr, width, total)
}
