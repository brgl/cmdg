package main

import (
	"os"
	"path"
	"strings"
)

const searchHistoryFile = "search_history"

func searchHistoryPath() string {
	return path.Join(path.Dir(configFilePath()), searchHistoryFile)
}

func loadSearchHistory() []string {
	data, err := os.ReadFile(searchHistoryPath())
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var ret []string
	for _, l := range lines {
		if l != "" {
			ret = append(ret, l)
		}
	}
	return ret
}

func addToHistory(query string) {
	if query == "" {
		return
	}
	history := loadSearchHistory()
	// Remove duplicate if exists.
	var filtered []string
	for _, h := range history {
		if h != query {
			filtered = append(filtered, h)
		}
	}
	filtered = append(filtered, query)
	data := strings.Join(filtered, "\n") + "\n"
	_ = os.WriteFile(searchHistoryPath(), []byte(data), 0600)
}

func removeFromHistory(query string) {
	history := loadSearchHistory()
	var filtered []string
	for _, h := range history {
		if h != query {
			filtered = append(filtered, h)
		}
	}
	if len(filtered) == 0 {
		_ = os.Remove(searchHistoryPath())
		return
	}
	data := strings.Join(filtered, "\n") + "\n"
	_ = os.WriteFile(searchHistoryPath(), []byte(data), 0600)
}
