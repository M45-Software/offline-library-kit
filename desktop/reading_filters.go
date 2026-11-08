package main

import "strings"

// These describe a collection, not the safety or subject of every page in it.
func collectionReadingProfile(item localCollection) (kind, topic string) {
	t := strings.ToLower(item.Name + " " + item.Title)
	kind, topic = "other", "reference"
	switch {
	case containsReadingWord(t, []string{"wikipedia", "wikimed", "wiktionary", "wikivoyage"}):
		kind = "reference"
	case strings.Contains(t, "gutenberg"), strings.Contains(t, "wikibooks"), strings.Contains(t, "wikisource"):
		kind, topic = "books", "books"
	case strings.Contains(t, "ifixit"), strings.Contains(t, "wikihow"):
		kind, topic = "guides", "repair"
	case strings.Contains(t, "phet"):
		kind, topic = "interactive", "learning"
	case strings.Contains(t, "maps"):
		kind, topic = "maps", "travel"
	case strings.Contains(t, "stackexchange"), strings.Contains(t, "q&a"), strings.Contains(t, "stackoverflow"):
		kind = "questions"
	}
	switch {
	case containsReadingWord(t, []string{"gardening", "cooking", "sustainable", "agriculture"}):
		topic = "food"
	case containsReadingWord(t, []string{"bicycles", "mechanic", "home_improvement", "home improvement", "woodworking", "ifixit"}):
		topic = "repair"
	case containsReadingWord(t, []string{"arduino", "raspberry", "electronics", "unix", "server fault", "super user"}):
		topic = "technology"
	case strings.Contains(t, "wikivoyage"):
		topic = "travel"
	case containsReadingWord(t, []string{"biology", "engineering", "phet"}):
		topic = "learning"
	}
	return
}
func containsReadingWord(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
func validReadingFilters(kind, topic string) bool {
	valid := func(value, allowed string) bool {
		if value == "" {
			return true
		}
		for _, option := range strings.Split(allowed, "|") {
			if value == option {
				return true
			}
		}
		return false
	}
	return valid(kind, "books|guides|questions|reference|interactive|maps|other") && valid(topic, "repair|food|learning|books|travel|reference|technology")
}
func matchesReadingFilters(item localCollection, kind, topic string) bool {
	k, t := collectionReadingProfile(item)
	return (kind == "" || kind == k) && (topic == "" || topic == t)
}
