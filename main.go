package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

func downloadAlbum(albumURL string, downloadsDir string, finalizeWithZip bool, isCandid bool) {
	pageSource := getContents(albumURL)
	rawBytes, err := io.ReadAll(pageSource)
	if err != nil {
		panic(err)
	}

	info := parsePageInfo(getTitle(bytes.NewReader(rawBytes)))

	if isCandid || info.IsCandid {
		if isCandid && info.PostName == "" && info.AlbumName != "" {
			info.PostName = info.AlbumName
			info.AlbumName = ""
		}
		downloadCandidPost(albumURL, rawBytes, info, downloadsDir)
		return
	}

	downloadProperAlbum(albumURL, rawBytes, info, downloadsDir, finalizeWithZip)
}

func downloadProperAlbum(albumURL string, rawBytes []byte, info PageInfo, downloadsDir string, finalizeWithZip bool) {
	urlParts := strings.Split(strings.TrimSuffix(albumURL, "/"), "/")
	albumID := ""
	for i, p := range urlParts {
		if p == "album" && i+1 < len(urlParts) {
			albumID = urlParts[i+1]
			break
		}
	}

	imagesFound := crawlAlbumImages(bytes.NewReader(rawBytes))
	albumDate, dateErr := getAlbumDate(bytes.NewReader(rawBytes))

	fmt.Printf("Found %q set from %s — %d image(s). Downloading...\n", info.AlbumName, info.ModelName, len(imagesFound))

	bucket := getBucket(info.ModelName)
	albumDir := fmt.Sprintf("%s/photos/%s/%s/%s", downloadsDir, bucket, info.ModelName, info.AlbumName)
	checkAndCreateDir(albumDir)

	var wg sync.WaitGroup
	var mu sync.Mutex
	imagesDownloaded := make([]string, len(imagesFound))
	total := len(imagesFound)

	for i, imageURL := range imagesFound {
		wg.Add(1)
		go func(i int, imageURL string) {
			defer wg.Done()
			imageOutputBase := albumDir + "/" + albumID + " - " + fmt.Sprintf("%04d", i+1)
			b, finalPath, err := saveImage(imageURL, imageOutputBase)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fmt.Printf("[%04d/%04d] — error: %v\n", i+1, total, err)
				return
			}
			imagesDownloaded[i] = finalPath
			fmt.Printf("[%04d/%04d] — %.2f MB\n", i+1, total, float64(b)/1024/1024)
		}(i, imageURL)
	}

	wg.Wait()

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
		if err := ZipFiles(albumDir+"/"+info.AlbumName+".zip", filtered); err != nil {
			panic(err)
		}
	}

	fmt.Println("Done!")
}

func downloadCandidPost(albumURL string, rawBytes []byte, info PageInfo, downloadsDir string) {
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
		fmt.Printf("Candid post %s/%s — no images found, skipping\n", modelName, postID)
		return
	}

	bucket := getBucket(modelName)
	modelDir := fmt.Sprintf("%s/candids/%s/%s", downloadsDir, bucket, modelName)

	// Skip if already downloaded.
	if entries, err := os.ReadDir(modelDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), postID) {
				fmt.Printf("[skip] Candid post %s/%s — already on disk\n", modelName, postID)
				return
			}
		}
	}

	checkAndCreateDir(modelDir)

	fmt.Printf("Candid post %s/%s (%s) — %d image(s)\n", modelName, postID, postName, len(imagesFound))

	if len(imagesFound) == 1 {
		imageOutputBase := fmt.Sprintf("%s/%s - %s - 0001", modelDir, postID, postName)
		b, _, err := saveImage(imagesFound[0], imageOutputBase)
		if err != nil {
			fmt.Printf("[0001/0001] — error: %v\n", err)
			return
		}
		fmt.Printf("[0001/0001] — %.2f MB\n", float64(b)/1024/1024)
		return
	}

	postDir := fmt.Sprintf("%s/%s - %s", modelDir, postID, postName)
	checkAndCreateDir(postDir)

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := len(imagesFound)

	for i, imageURL := range imagesFound {
		wg.Add(1)
		go func(i int, imageURL string) {
			defer wg.Done()
			imageOutputBase := fmt.Sprintf("%s/%s - %s - %04d", postDir, postID, postName, i+1)
			b, _, err := saveImage(imageURL, imageOutputBase)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fmt.Printf("[%04d/%04d] — error: %v\n", i+1, total, err)
				return
			}
			fmt.Printf("[%04d/%04d] — %.2f MB\n", i+1, total, float64(b)/1024/1024)
		}(i, imageURL)
	}

	wg.Wait()
}

func downloadBlogPost(postURL string, downloadsDir string) {
	downloadAlbum(postURL, downloadsDir, false, true)
}

func downloadGroupThread(threadURL string, downloadsDir string) {
	pageSource := getContents(threadURL)
	rawBytes, err := io.ReadAll(pageSource)
	if err != nil {
		panic(err)
	}

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
	checkAndCreateDir(threadDir)

	existingEntries, _ := os.ReadDir(threadDir)

	for _, bucket := range buckets {
		if len(bucket.Images) == 0 {
			continue
		}

		if bucket.CommentID != "" {
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
				continue
			}
		}

		commentSnippet := truncateName(sanitizeName(bucket.CommentText), 60)
		var baseName string
		if commentSnippet != "" {
			baseName = fmt.Sprintf("%s - %s - %s", bucket.CommentID, bucket.Username, commentSnippet)
		} else {
			baseName = fmt.Sprintf("%s - %s", bucket.CommentID, bucket.Username)
		}

		total := len(bucket.Images)
		if total == 1 {
			imageOutputBase := fmt.Sprintf("%s/%s - 0001", threadDir, baseName)
			b, _, err := saveImage(bucket.Images[0], imageOutputBase)
			if err != nil {
				fmt.Printf("%s [0001/0001] — error: %v\n", baseName, err)
				continue
			}
			fmt.Printf("%s [0001/0001] — %.2f MB\n", baseName, float64(b)/1024/1024)
			continue
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		for i, imageURL := range bucket.Images {
			wg.Add(1)
			go func(i int, imageURL string) {
				defer wg.Done()
				imageOutputBase := fmt.Sprintf("%s/%s - %04d", threadDir, baseName, i+1)
				b, _, err := saveImage(imageURL, imageOutputBase)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					fmt.Printf("%s [%04d/%04d] — error: %v\n", baseName, i+1, total, err)
					return
				}
				fmt.Printf("%s [%04d/%04d] — %.2f MB\n", baseName, i+1, total, float64(b)/1024/1024)
			}(i, imageURL)
		}
		wg.Wait()
	}
}

func main() {
	err := godotenv.Load()
	if err != nil {
		panic(err)
	}

	downloadsDir := os.Getenv("DOWNLOADSDIR")
	args := os.Args

	// --- GLOBAL AND LOCAL SEARCH CONTEXT INTERCEPTOR ---
	if len(args) >= 3 && args[1] == "search" {
		if len(args) == 3 {
			targetUser := args[2]
			runGlobalSearch(targetUser, downloadsDir)
		} else {
			threadURL := args[2]
			targetUser := args[3]
			executeSearchAndDownload(threadURL, targetUser, downloadsDir)
		}
		return
	}
	// ----------------------------------------------------

	if len(args) < 2 {
		panic("usage: SGo-Scraper <url> [-z] OR SGo-Scraper search [url] <username>")
	}

	albumURL := args[1]
	finalizeWithZip := args[len(args)-1] == "-z"

	checkAndCreateDir(downloadsDir)

	switch {
	case strings.Contains(albumURL, "/groups/") && strings.Contains(albumURL, "/thread/"):
		downloadGroupThread(albumURL, downloadsDir)

	case strings.Contains(albumURL, "/videos/"):
		downloadVideoPost(albumURL, downloadsDir, "")

	case strings.Contains(albumURL, "/album/"):
		downloadAlbum(albumURL, downloadsDir, finalizeWithZip, false)

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
		fmt.Println("Found", len(albumLinks), "albums")
		isCandid := strings.Contains(albumURL, "/candids/")
		for _, link := range albumLinks {
			downloadAlbum(link, downloadsDir, finalizeWithZip, isCandid)
		}

	default:
		parts := strings.Split(strings.TrimSuffix(albumURL, "/"), "/")
		modelName := parts[len(parts)-1]
		base := strings.TrimSuffix(albumURL, "/")
		fmt.Printf("Content base: %s (model: %s)\n", albumURL, modelName)

		seen := map[string]bool{}

		candidLinks := getAllAlbumLinks(base+"/photos/view/candids/", modelName)
		fmt.Println("Found", len(candidLinks), "candid posts")
		for _, link := range candidLinks {
			seen[link] = true
			downloadAlbum(link, downloadsDir, finalizeWithZip, true)
		}

		videoLinks := getAllVideoLinks(base + "/videos/")
		fmt.Println("Found", len(videoLinks), "videos")
		for _, link := range videoLinks {
			seen[link] = true
			downloadVideoPost(link, downloadsDir, modelName)
		}

		blogLinks := getAllBlogLinks(base+"/blog/", modelName)
		fmt.Println("Found", len(blogLinks), "blog posts")
		for _, link := range blogLinks {
			if seen[link] {
				continue
			}
			seen[link] = true
			downloadBlogPost(link, downloadsDir)
		}

		fmt.Println("Done!")
	}
}