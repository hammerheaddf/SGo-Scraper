package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
)

func downloadAlbum(albumURL string, downloadsDir string, finalizeWithZip bool, isCandid bool, currIndex, totalCount int) {
	rawBytes := getContents(albumURL)

	info := parsePageInfo(getTitle(bytes.NewReader(rawBytes)))

	if totalCount <= 1 && extractModelFromURL(albumURL) == "" && info.ModelName != "" {
		fmt.Println("Model:", info.ModelName)
		fmt.Println("ModelDir:", filepath.Join(downloadsDir, info.ModelName))
		fmt.Println()
	}

	if isCandid || info.IsCandid {
		if isCandid && info.PostName == "" && info.AlbumName != "" {
			info.PostName = info.AlbumName
			info.AlbumName = ""
		}
		downloadCandidPost(albumURL, rawBytes, info, downloadsDir, currIndex, totalCount)
		return
	}

	downloadProperAlbum(albumURL, rawBytes, info, downloadsDir, finalizeWithZip, currIndex, totalCount)
}

func downloadProperAlbum(albumURL string, rawBytes []byte, info PageInfo, downloadsDir string, finalizeWithZip bool, currIndex, totalCount int) {
	urlParts := strings.Split(strings.TrimSuffix(albumURL, "/"), "/")
	albumID := ""
	for i, p := range urlParts {
		if p == "album" && i+1 < len(urlParts) {
			albumID = urlParts[i+1]
			break
		}
	}

	db, dbErr := getModelDB(info.ModelName)
	if dbErr == nil {
		defer db.Close()
		if isDownloaded(db, "album", albumID) {
			idxStr := formatIndex(currIndex, totalCount)
			fmt.Printf("%s[skip] Album %s/%s — already in database\n", idxStr, info.ModelName, albumID)
			return
		}
	}

	imagesFound := crawlAlbumImages(bytes.NewReader(rawBytes))
	albumDate, dateErr := getAlbumDate(bytes.NewReader(rawBytes))

	idxStr := formatIndex(currIndex, totalCount)
	fmt.Printf("%sFound %q set from %s — %d image(s). Downloading...\n", idxStr, info.AlbumName, info.ModelName, len(imagesFound))

	albumDir := filepath.Join(downloadsDir, info.ModelName, "photos", info.ModelName+" - "+info.AlbumName)
	fmt.Println("AlbumDir:", albumDir) //debug info for looking directory name.
	checkAndCreateDir(albumDir)

	var wg sync.WaitGroup
	var mu sync.Mutex
	imagesDownloaded := make([]string, len(imagesFound))
	total := len(imagesFound)
	sem := make(chan struct{}, 5) // limit to 5 simultaneous downloads

	completed := 0
	var totalBytes int64

	printProgress("Downloading", 0, total, 0)
	for i, imageURL := range imagesFound {
		sem <- struct{}{} // acquire slot before spawning
		if i > 0 {
			time.Sleep(500 * time.Millisecond) // delay between download starts
		}
		wg.Add(1)
		go func(i int, imageURL string) {
			defer wg.Done()
			defer func() { <-sem }() // release slot when finished
			
			imageOutput := filepath.Join(albumDir, fmt.Sprintf("%s - %04d.jpg", albumID, i+1))
			b, err := saveImage(imageURL, imageOutput)
			
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fmt.Printf("\r%s\rError: [%04d/%04d] — %v\n", strings.Repeat(" ", 85), i+1, total, err)
			} else {
				imagesDownloaded[i] = imageOutput
				totalBytes += b
			}
			completed++
			printProgress("Downloading", completed, total, totalBytes)
		}(i, imageURL)
	}

	wg.Wait()

	anySuccess := false
	for _, f := range imagesDownloaded {
		if f != "" {
			anySuccess = true
			break
		}
	}
	if anySuccess {
		if db, err := getModelDB(info.ModelName); err == nil {
			defer db.Close()
			markDownloaded(db, "album", albumID, info.AlbumName)
		}
	}

	if dateErr == nil {
		for _, imgPath := range imagesDownloaded {
			if imgPath != "" {
				os.Chtimes(imgPath, albumDate, albumDate)
			}
		}
	} else {
		fmt.Println("Warning: could not determine album date:", dateErr)
	}

	if finalizeWithZip {
		var filtered []string
		for _, f := range imagesDownloaded {
			if f != "" {
				filtered = append(filtered, f)
			}
		}
		if err := ZipFiles(filepath.Join(albumDir, info.AlbumName+".zip"), filtered); err != nil {
			panic(err)
		}
	}

	//fmt.Println("Albums Done.")
	fmt.Println()
}

func downloadCandidPost(albumURL string, rawBytes []byte, info PageInfo, downloadsDir string, currIndex, totalCount int) {
	parts := strings.Split(strings.TrimSuffix(albumURL, "/"), "/")

	postID := ""
	urlSlug := ""
	for i, p := range parts {
		switch p {
		case "album", "blog":
			if i+1 < len(parts) {
				postID = parts[i+1]
				if i+2 < len(parts) && parts[i+2] != "" {
					urlSlug = sanitizeName(parts[i+2])
				}
				if postID != "" {
					break
				}
			}
		}
		if postID != "" {
			break
		}
	}
	if postID == "" {
		postID = sanitizeName(parts[len(parts)-1])
	}

	modelName := info.ModelName
	if modelName == "" {
		for i, p := range parts {
			if (p == "girls" || p == "members") && i+1 < len(parts) {
				rawModel := sanitizeName(parts[i+1])
				if rawModel != "" {
					modelName = strings.ToUpper(rawModel[:1]) + rawModel[1:]
				}
				break
			}
		}
	}

	postName := info.PostName
	if postName == "" {
		postName = urlSlug
	}
	if postName == "" {
		postName = info.AlbumName
	}
	if postName == "" {
		postName = postID
	}
	postName = truncateName(postName, 80)

	// Use the API first; it returns permanent /cache/ URLs, not expiring /temp/ ones.
	imagesFound := getAlbumInfoImages(postID)
	if len(imagesFound) == 0 {
		imagesFound = crawlCacheImages(bytes.NewReader(rawBytes))
	}
	if len(imagesFound) == 0 {
		imagesFound = crawlAlbumImages(bytes.NewReader(rawBytes))
	}
	if len(imagesFound) == 0 {
		imagesFound = crawlCandidImages(bytes.NewReader(rawBytes))
	}
	if len(imagesFound) == 0 {
		imagesFound = crawlBlogImagesRegex(rawBytes)
	}
	if len(imagesFound) == 0 {
		idxStr := formatIndex(currIndex, totalCount)
		fmt.Printf("%sCandid post %s/%s — no images found, skipping\n", idxStr, modelName, postID)
		return
	}

	db, dbErr := getModelDB(modelName)
	if dbErr == nil {
		if isDownloaded(db, "candid", postID) {
			db.Close()
			idxStr := formatIndex(currIndex, totalCount)
			fmt.Printf("%s[skip] Candid post %s/%s — already in database\n", idxStr, modelName, postID)
			return
		}
	}

	modelDir := filepath.Join(downloadsDir, modelName, "candids")

	// Skip if already downloaded.
	if entries, err := os.ReadDir(modelDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), postID) {
				idxStr := formatIndex(currIndex, totalCount)
				fmt.Printf("%s[skip] Candid post %s/%s — already on disk\n", idxStr, modelName, postID)
				if dbErr == nil {
					markDownloaded(db, "candid", postID, postName)
					db.Close()
				}
				return
			}
		}
	}
	if dbErr == nil {
		db.Close()
	}
	fmt.Println("ModelDir:", modelDir) //debug info for looking directory name.
	fmt.Println()
	checkAndCreateDir(modelDir)

	idxStr := formatIndex(currIndex, totalCount)
	fmt.Printf("%sCandid post %s/%s (%s) — %d image(s)\n", idxStr, modelName, postID, postName, len(imagesFound))

	if len(imagesFound) == 1 {
		printProgress("Downloading", 0, 1, 0)
		imageOutput := filepath.Join(modelDir, fmt.Sprintf("%s - %s - 0001.jpg", postID, postName))
		b, err := saveImage(imagesFound[0], imageOutput)
		if err != nil {
			fmt.Printf("\r%s\rError: [0001/0001] — %v\n", strings.Repeat(" ", 85), err)
			return
		}
		printProgress("Downloading", 1, 1, b)
		if db, err := getModelDB(modelName); err == nil {
			defer db.Close()
			markDownloaded(db, "candid", postID, postName)
		}
		//fmt.Println("Done!")
		fmt.Println()
		return
	}

	postDir := filepath.Join(modelDir, fmt.Sprintf("%s - %s", postID, postName))
	fmt.Println("PostDir:", postDir) //debug info for looking directory name.
	checkAndCreateDir(postDir)

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := len(imagesFound)
	sem := make(chan struct{}, 5) // limit to 5 simultaneous downloads

	completed := 0
	var totalBytes int64

	printProgress("Downloading", 0, total, 0)
	for i, imageURL := range imagesFound {
		sem <- struct{}{} // acquire slot before spawning
		if i > 0 {
			time.Sleep(500 * time.Millisecond) // delay between download starts
		}
		wg.Add(1)
		go func(i int, imageURL string) {
			defer wg.Done()
			defer func() { <-sem }() // release slot when finished
			
			imageOutput := filepath.Join(postDir, fmt.Sprintf("%s - %s - %04d.jpg", postID, postName, i+1))
			b, err := saveImage(imageURL, imageOutput)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fmt.Printf("\r%s\rError: [%04d/%04d] — %v\n", strings.Repeat(" ", 85), i+1, total, err)
			} else {
				totalBytes += b
			}
			completed++
			printProgress("Downloading", completed, total, totalBytes)
		}(i, imageURL)
	}

	wg.Wait()
	if db, err := getModelDB(modelName); err == nil {
		defer db.Close()
		markDownloaded(db, "candid", postID, postName)
	}
	//fmt.Println("Done!")
	fmt.Println()
}

func downloadBlogPost(postURL string, downloadsDir string, currIndex, totalCount int) {
	downloadAlbum(postURL, downloadsDir, false, true, currIndex, totalCount)
}

func downloadGroupThread(threadURL string, downloadsDir string) {
	rawBytes := getContents(threadURL)

	parts := strings.Split(strings.TrimSuffix(threadURL, "/"), "/")
	groupName := "group"
	threadID := "thread"
	for i, p := range parts {
		if p == "groups" && i+1 < len(parts) {
			groupName = sanitizeName(parts[i+1])
		}
		if p == "thread" && i+1 < len(parts) {
			threadID = sanitizeName(parts[i+1])
		}
	}

	rawTitle := strings.TrimSpace(strings.Split(getTitle(bytes.NewReader(rawBytes)), " by ")[0])
	threadTitle := sanitizeName(rawTitle)
	if threadTitle == "" {
		threadTitle = threadID
	}
	threadTitle = truncateName(threadTitle, 60)

	threadDir := fmt.Sprintf("%s/groups/%s/%s - %s", downloadsDir, groupName, threadID, threadTitle)

	buckets := getAllGroupThreadImageBuckets(threadURL)
	if len(buckets) == 0 {
		fmt.Printf("Group thread %s/%s — no images found, skipping\n", groupName, threadID)
		return
	}

	fmt.Printf("Group thread %s/%s (%s) — %d post(s) with images\n", groupName, threadID, threadTitle, len(buckets))
	fmt.Println("ThreadDir:", threadDir) //debug info for looking directory name.
	checkAndCreateDir(threadDir)

	existingEntries, _ := os.ReadDir(threadDir)

	for _, bucket := range buckets {
		if len(bucket.Images) == 0 {
			continue
		}

		commentSnippet := truncateName(sanitizeName(bucket.CommentText), 60)

		if bucket.CommentID != "" {
			db, dbErr := getGroupDB()
			if dbErr == nil {
				if isDownloaded(db, "group_comment", bucket.CommentID) {
					db.Close()
					fmt.Printf("[skip] Group thread %s/%s — comment %s already in database\n", groupName, threadID, bucket.CommentID)
					continue
				}
			}

			prefix := bucket.CommentID + " - "
			alreadyOnDisk := false
			for _, e := range existingEntries {
				if strings.HasPrefix(e.Name(), prefix) {
					alreadyOnDisk = true
					break
				}
			}
			if alreadyOnDisk {
				fmt.Printf("[skip] Group thread %s/%s — comment %s already on disk\n", groupName, threadID, bucket.CommentID)
				if dbErr == nil {
					markDownloaded(db, "group_comment", bucket.CommentID, commentSnippet)
					db.Close()
				}
				continue
			}
			if dbErr == nil {
				db.Close()
			}
		}

		var baseName string
		if commentSnippet != "" {
			baseName = fmt.Sprintf("%s - %s - %s", bucket.CommentID, bucket.Username, commentSnippet)
		} else {
			baseName = fmt.Sprintf("%s - %s", bucket.CommentID, bucket.Username)
		}

		total := len(bucket.Images)
		if total == 1 {
			printProgress(baseName, 0, 1, 0)
			imageOutput := fmt.Sprintf("%s/%s - 0001.jpg", threadDir, baseName)
			b, err := saveImage(bucket.Images[0], imageOutput)
			if err != nil {
				fmt.Printf("\r%s\rError: %s [0001/0001] — %v\n", strings.Repeat(" ", 85), baseName, err)
				continue
			}
			printProgress(baseName, 1, 1, b)
			if bucket.CommentID != "" {
				if db, err := getGroupDB(); err == nil {
					defer db.Close()
					markDownloaded(db, "group_comment", bucket.CommentID, commentSnippet)
				}
			}
			continue
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		completed := 0
		var totalBytes int64
		
		sem := make(chan struct{}, 5) // limit to 5 simultaneous downloads
		
		printProgress(baseName, 0, total, 0)
		for i, imageURL := range bucket.Images {
			sem <- struct{}{} // acquire slot before spawning
			if i > 0 {
				time.Sleep(500 * time.Millisecond) // delay between download starts
			}
			wg.Add(1)
			go func(i int, imageURL string) {
				defer wg.Done()
				defer func() { <-sem }() // release slot when finished
				
				imageOutput := fmt.Sprintf("%s/%s - %04d.jpg", threadDir, baseName, i+1)
				b, err := saveImage(imageURL, imageOutput)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					fmt.Printf("\r%s\rError: %s [%04d/%04d] — %v\n", strings.Repeat(" ", 85), baseName, i+1, total, err)
				} else {
					totalBytes += b
				}
				completed++
				printProgress(baseName, completed, total, totalBytes)
			}(i, imageURL)
		}
		wg.Wait()
		if bucket.CommentID != "" {
			if db, err := getGroupDB(); err == nil {
				defer db.Close()
				markDownloaded(db, "group_comment", bucket.CommentID, commentSnippet)
			}
		}
	}
}

func extractModelFromURL(urlStr string) string {
	parts := strings.Split(strings.TrimSuffix(urlStr, "/"), "/")
	for i, p := range parts {
		if (p == "girls" || p == "members") && i+1 < len(parts) {
			return titleCaseModelName(parts[i+1])
		}
	}
	return ""
}

func main() {
	err := godotenv.Load()
	if err != nil {
		panic(err)
	}

	downloadsDir := os.Getenv("DOWNLOADSDIR")
	args := os.Args
	if len(args) < 2 {
		panic("usage: SGo-Scraper <url> [-z]")
	}

	albumURL := args[1]
	finalizeWithZip := args[len(args)-1] == "-z"

	fmt.Println("[" + time.Now().Format("2006-01-02 15:04:05") + "]")
	fmt.Println("URL:", albumURL)
	modelName := extractModelFromURL(albumURL)
	if modelName != "" {
		fmt.Println("MODEL:", modelName)
		fmt.Println("ModelDir:", filepath.Join(downloadsDir, modelName))
	}
	fmt.Println()

	checkAndCreateDir(downloadsDir)

	switch {
	case strings.Contains(albumURL, "/groups/") && strings.Contains(albumURL, "/thread/"):
		downloadGroupThread(albumURL, downloadsDir)

	case strings.Contains(albumURL, "/videos/"):
		downloadVideoPost(albumURL, downloadsDir, "", 1, 1)

	case strings.Contains(albumURL, "/album/"):
		downloadAlbum(albumURL, downloadsDir, finalizeWithZip, false, 1, 1)

	case strings.Contains(albumURL, "/photos"):
		photoParts := strings.Split(strings.TrimSuffix(albumURL, "/"), "/")
		photoModel := ""
		for i, p := range photoParts {
			if (p == "girls" || p == "members") && i+1 < len(photoParts) {
				photoModel = photoParts[i+1]
				break
			}
		}
		albumLinks := getAllAlbumLinks(albumURL, photoModel)
		isCandid := strings.Contains(albumURL, "/candids/")
		heading := "Photosets"
		if isCandid {
			heading = "Candid Posts"
		}
		fmt.Printf("Found %d %s\n", len(albumLinks), heading)
		for idx, link := range albumLinks {
			downloadAlbum(link, downloadsDir, finalizeWithZip, isCandid, idx+1, len(albumLinks))
		}
		if photoModel != "" {
			fmt.Printf("%s Finished Downloading...\n", titleCaseModelName(photoModel))
		}

	default:
		parts := strings.Split(strings.TrimSuffix(albumURL, "/"), "/")
		modelName := parts[len(parts)-1]
		base := strings.TrimSuffix(albumURL, "/")
		normalizedModelName := titleCaseModelName(modelName)

		seen := map[string]bool{}

		photosetLinks := getAllAlbumLinks(base+"/photos/view/photosets/", modelName)
		fmt.Println("Found", len(photosetLinks), "Photosets")
		for idx, link := range photosetLinks {
			seen[link] = true
			downloadAlbum(link, downloadsDir, finalizeWithZip, false, idx+1, len(photosetLinks))
		}

		fmt.Println()
		candidLinks := getAllAlbumLinks(base+"/photos/view/candids/", modelName)
		fmt.Println("Found", len(candidLinks), "Candid Posts")
		for idx, link := range candidLinks {
			if seen[link] {
				continue
			}
			seen[link] = true
			downloadAlbum(link, downloadsDir, finalizeWithZip, true, idx+1, len(candidLinks))
		}

		fmt.Println()
		videoLinks := getAllVideoLinks(base + "/videos/")
		fmt.Println("Found", len(videoLinks), "Videos")
		for idx, link := range videoLinks {
			if seen[link] {
				continue
			}
			seen[link] = true
			downloadVideoPost(link, downloadsDir, modelName, idx+1, len(videoLinks))
		}

		fmt.Println()
		blogLinks := getAllBlogLinks(base+"/blog/", modelName)
		fmt.Println("Found", len(blogLinks), "Blog Posts")
		for idx, link := range blogLinks {
			if seen[link] {
				continue
			}
			seen[link] = true
			downloadBlogPost(link, downloadsDir, idx+1, len(blogLinks))
		}

		fmt.Println()
		fmt.Printf("%s Finished Downloading...\n", normalizedModelName)
	}
}

func printProgress(prefix string, completed, total int, totalBytes int64) {
	if total <= 0 {
		return
	}
	width := 30
	percent := float64(completed) / float64(total)
	filled := int(percent * float64(width))
	if filled > width {
		filled = width
	}

	var bar string
	if filled == width {
		bar = strings.Repeat("=", width)
	} else if filled > 0 {
		bar = strings.Repeat("=", filled-1) + ">" + strings.Repeat(" ", width-filled)
	} else {
		bar = strings.Repeat(" ", width)
	}

	sizeMB := float64(totalBytes) / 1024 / 1024
	fmt.Printf("\r%s: [%s] %d/%d (%d%%) | %.2f MB", prefix, bar, completed, total, int(percent*100), sizeMB)
	if completed == total {
		fmt.Println()
	}
}
