package utils

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/model"
	"gopkg.in/yaml.v3"
)

func LoadMirrors() ([]model.Mirror, error) {
	var data model.MirrorData
	fileData, err := os.ReadFile("mirrors.yml")
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(fileData, &data)
	if err != nil {
		return nil, err
	}
	return data.Mirrors, nil
}

func CheckMirrorStatus(url string) (int, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Head(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func PingMirror(url string) (time.Duration, error) {
	start := time.Now()
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return time.Since(start), nil
}

func NormalizeMirrorName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "")
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, "_", "")
	return strings.TrimSpace(name)
}

func MatchesMirrorName(mirrorName, input string) bool {
	normalizedMirror := NormalizeMirrorName(mirrorName)
	normalizedInput := NormalizeMirrorName(input)

	// Exact match after normalization
	if normalizedMirror == normalizedInput {
		return true
	}

	// Prefix match (e.g. "arvancloud" matches "arvancloudlinux")
	if strings.HasPrefix(normalizedMirror, normalizedInput) {
		return true
	}

	return false
}
