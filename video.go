package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var ogTitleRe = regexp.MustCompile(`(?i)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)["']`)

func titleCaseModelName(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "/"))
	if s == "" {
		return "Unknown"
	}
	parts := strings.Fields(strings.ReplaceAll(s, "-", " "))
	for i, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(strings.ToLower(p))
		if len(r) == 0 {
			continue
		}
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		parts[i] = string(r)
	}
	return strings.Join(parts, " ")
}

func parseVideoInfo(videoURL string, rawBytes []byte, expectedModel string) (string, string, string) {
	parts := strings.Split(strings.TrimSuffix(videoURL, "/"), "/")
	videoID := ""
	videoSlug := ""
	for i, p := range parts {
		if p == "videos" && i+1 < len(parts) {
			videoID = parts[i+1]
			if i+2 < len(parts) && parts[i+2] != "" {
				videoSlug = sanitizeName(parts[i+2])
			}
			break
		}
	}
	if videoID == "" {
		videoID = sanitizeName(parts[len(parts)-1])
	}

	htmlStr := string(rawBytes)
	postTitle := ""
	modelName := titleCaseModelName(expectedModel)
	modelName = sanitizeName(modelName)

	if m := ogTitleRe.FindStringSubmatch(htmlStr); len(m) > 1 {
		og := strings.TrimSpace(m[1])
		if idx := strings.LastIndex(og, " by "); idx != -1 {
			postTitle = sanitizeName(strings.TrimSpace(og[:idx]))
			if modelName == "Unknown" || modelName == "" {
				rest := strings.TrimSpace(og[idx+len(" by "):])
				rest = strings.Split(rest, " | ")[0]
				rest = strings.Split(rest, " - ")[0]
				modelName = titleCaseModelName(rest)
			}
		} else {
			var ogParts []string
			if strings.Contains(og, " | ") {
				ogParts = strings.Split(og, " | ")
			} else if strings.Contains(og, " - ") {
				ogParts = strings.Split(og, " - ")
			}
			if len(ogParts) > 1 {
				postTitle = sanitizeName(ogParts[0])
				if modelName == "Unknown" || modelName == "" {
					candidate := strings.TrimSpace(ogParts[1])
					if !strings.EqualFold(candidate, "SuicideGirls") && !strings.EqualFold(candidate, "SGirls") {
						modelName = titleCaseModelName(candidate)
					} else if len(ogParts) > 2 {
						modelName = titleCaseModelName(ogParts[2])
					}
				}
			} else {
				postTitle = sanitizeName(og)
			}
		}
	}

	if postTitle == "" {
		title := strings.TrimSpace(getTitle(bytes.NewReader(rawBytes)))
		if idx := strings.LastIndex(title, " by "); idx != -1 {
			postTitle = sanitizeName(strings.TrimSpace(title[:idx]))
			if modelName == "Unknown" || modelName == "" {
				rest := strings.TrimSpace(title[idx+len(" by "):])
				rest = strings.Split(rest, " | ")[0]
				rest = strings.Split(rest, " - ")[0]
				modelName = titleCaseModelName(rest)
			}
		} else {
			var titleParts []string
			if strings.Contains(title, " | ") {
				titleParts = strings.Split(title, " | ")
			} else if strings.Contains(title, " - ") {
				titleParts = strings.Split(title, " - ")
			}
			if len(titleParts) > 1 {
				postTitle = sanitizeName(titleParts[0])
				if modelName == "Unknown" || modelName == "" {
					candidate := strings.TrimSpace(titleParts[1])
					if !strings.EqualFold(candidate, "SuicideGirls") && !strings.EqualFold(candidate, "SGirls") {
						modelName = titleCaseModelName(candidate)
					}
				}
			}
		}
	}

	if postTitle == "" {
		postTitle = videoSlug
	}
	if postTitle == "" {
		postTitle = videoID
	}

	// Fallback to scanning page HTML for model profile links if modelName is still Unknown/empty
	if modelName == "Unknown" || modelName == "" {
		loggedInUser := ""
		userRe := regexp.MustCompile(`sg-user_name="([^"]+)"`)
		if um := userRe.FindSubmatch(rawBytes); len(um) > 1 {
			loggedInUser = string(um[1])
		}

		allMatches := memberHrefRe.FindAllSubmatch(rawBytes, -1)
		for _, m := range allMatches {
			if len(m) > 1 {
				candidate := string(m[1])
				if loggedInUser != "" && strings.EqualFold(candidate, loggedInUser) {
					continue
				}
				modelName = titleCaseModelName(candidate)
				break
			}
		}
	}

	return videoID, truncateName(postTitle, 80), truncateName(modelName, 80)
}

func downloadVideoPost(videoURL string, downloadsDir string, expectedModel string, currIndex, totalCount int) {
	rawBytes := getContents(videoURL)

	videoID, postTitle, modelName := parseVideoInfo(videoURL, rawBytes, expectedModel)

	db, dbErr := getModelDB(modelName)
	if dbErr == nil {
		if isDownloaded(db, "video", videoID) {
			db.Close()
			idxStr := formatIndex(currIndex, totalCount)
			fmt.Printf("%s[skip] Video %s/%s — already in database\n", idxStr, modelName, videoID)
			return
		}
	}

	modelDir := filepath.Join(downloadsDir, modelName, "videos")

	// Skip if already downloaded.
	if entries, err := os.ReadDir(modelDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), videoID) {
				idxStr := formatIndex(currIndex, totalCount)
				fmt.Printf("%s[skip] Video %s/%s — already on disk\n", idxStr, modelName, videoID)
				if dbErr == nil {
					markDownloaded(db, "video", videoID, postTitle)
					db.Close()
				}
				return
			}
		}
	}
	if dbErr == nil {
		db.Close()
	}

	streamURL := crawlVideoStream(bytes.NewReader(rawBytes))
	if streamURL == "" {
		idxStr := formatIndex(currIndex, totalCount)
		fmt.Printf("%sVideo %s/%s — could not find stream URL\n", idxStr, modelName, videoID)
		return
	}

	checkAndCreateDir(modelDir)
	idxStr := formatIndex(currIndex, totalCount)
	output := filepath.Join(modelDir, fmt.Sprintf("%s - %s.mp4", videoID, postTitle))

	fmt.Printf("%sVideo post %s/%s (%s) — downloading...\n", idxStr, modelName, videoID, postTitle)
	headers := "Referer: https://www.suicidegirls.com/\r\n" +
		"User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36\r\n"
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-headers", headers, "-y", "-i", streamURL, "-c", "copy", output)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Video %s/%s — ffmpeg failed: %v\n", modelName, videoID, err)
		// Remove any partial output so the skip-on-rerun check (prefix match
		// on videoID) doesn't treat a failed download as "already on disk".
		os.Remove(output)
		return
	}

	info, err := os.Stat(output)
	if err != nil {
		fmt.Printf("Video %s/%s — output file missing\n", modelName, videoID)
		return
	}

	if info.Size() == 0 {
		fmt.Printf("Video %s/%s — output file is empty\n", modelName, videoID)
		os.Remove(output)
		return
	}

	if db, err := getModelDB(modelName); err == nil {
		defer db.Close()
		markDownloaded(db, "video", videoID, postTitle)
	}

	//fmt.Println("Done!")
	fmt.Println()
}
