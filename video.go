package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	if m := ogTitleRe.FindStringSubmatch(htmlStr); len(m) > 1 {
		og := strings.TrimSpace(m[1])
		if idx := strings.LastIndex(og, " by "); idx != -1 {
			postTitle = sanitizeName(strings.TrimSpace(og[:idx]))
		} else {
			postTitle = sanitizeName(strings.SplitN(og, " | ", 2)[0])
		}
	}
	if postTitle == "" {
		title := strings.TrimSpace(getTitle(bytes.NewReader(rawBytes)))
		if idx := strings.LastIndex(title, " by "); idx != -1 {
			postTitle = sanitizeName(strings.TrimSpace(title[:idx]))
		}
	}
	if postTitle == "" {
		postTitle = videoSlug
	}
	if postTitle == "" {
		postTitle = videoID
	}

	modelName := titleCaseModelName(expectedModel)
	if modelName == "Unknown" || modelName == "" {
		title := strings.TrimSpace(getTitle(bytes.NewReader(rawBytes)))
		if idx := strings.LastIndex(title, " by "); idx != -1 {
			rest := strings.TrimSpace(title[idx+len(" by "):])
			modelName = titleCaseModelName(strings.SplitN(rest, " | ", 2)[0])
		}
	}

	return videoID, truncateName(postTitle, 80), truncateName(modelName, 80)
}

func downloadVideoPost(videoURL string, downloadsDir string, expectedModel string) {
	pageSource := getContents(videoURL)
	rawBytes, err := io.ReadAll(pageSource)
	if err != nil {
		panic(err)
	}

	videoID, postTitle, modelName := parseVideoInfo(videoURL, rawBytes, expectedModel)
	
	bucket := getBucket(modelName)
	modelDir := fmt.Sprintf("%s/candids/%s/%s", downloadsDir, bucket, modelName)

	// Skip if already downloaded.
	if entries, err := os.ReadDir(modelDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), videoID) {
				fmt.Printf("[skip] Video %s/%s — already on disk\n", modelName, videoID)
				return
			}
		}
	}

	streamURL := crawlVideoStream(bytes.NewReader(rawBytes))
	if streamURL == "" {
		fmt.Printf("Video %s/%s — no stream found, skipping\n", modelName, videoID)
		return
	}

	checkAndCreateDir(modelDir)
	output := fmt.Sprintf("%s/%s - %s.mp4", modelDir, videoID, postTitle)

	fmt.Printf("Video post %s/%s (%s) — downloading...\n", modelName, videoID, postTitle)
	headers := "Referer: https://www.suicidegirls.com/\r\n" +
		"User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36\r\n"
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-headers", headers, "-y", "-i", streamURL, "-c", "copy", output)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Video %s/%s — ffmpeg failed: %v\n", modelName, videoID, err)
		return
	}

	fmt.Println("Done!")
}