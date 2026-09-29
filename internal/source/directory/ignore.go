package directory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ignoreRule struct {
	base      string
	pattern   string
	negated   bool
	directory bool
	anchored  bool
}

func ancestorIgnoreRules(ctx context.Context, root string) ([]ignoreRule, error) {
	repository, found, err := findRepositoryRoot(root)
	if err != nil {
		return nil, err
	}
	if !found || repository == root {
		return nil, nil
	}
	relative, err := filepath.Rel(repository, root)
	if err != nil {
		return nil, fmt.Errorf("locate repository ignore rules: %w", err)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	var rules []ignoreRule
	directory := repository
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rules, err = appendIgnoreFile(ctx, rules, directory)
		if err != nil {
			return nil, err
		}
		directory = filepath.Join(directory, part)
	}
	return rules, nil
}

func findRepositoryRoot(root string) (string, bool, error) {
	for current := root; ; current = filepath.Dir(current) {
		_, err := os.Lstat(filepath.Join(current, ".git"))
		if err == nil {
			return current, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("inspect repository metadata in %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false, nil
		}
	}
}

func appendIgnoreFile(ctx context.Context, inherited []ignoreRule, directory string) ([]ignoreRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return inherited, nil
		}
		return nil, fmt.Errorf("read ignore rules %q: %w", path, err)
	}
	rules := append([]ignoreRule(nil), inherited...)
	for _, raw := range strings.Split(string(data), "\n") {
		line := trimUnescapedTrailingSpaces(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{base: directory}
		if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = line[1:]
		} else if strings.HasPrefix(line, "!") {
			rule.negated = true
			line = line[1:]
		}
		if strings.HasPrefix(line, "/") {
			rule.anchored = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") && !strings.HasSuffix(line, `\/`) {
			rule.directory = true
			line = strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}
		rule.pattern = line
		rules = append(rules, rule)
	}
	return rules, nil
}

func trimUnescapedTrailingSpaces(value string) string {
	for strings.HasSuffix(value, " ") {
		index := len(value) - 2
		backslashes := 0
		for index >= 0 && value[index] == '\\' {
			backslashes++
			index--
		}
		if backslashes%2 == 1 {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}

func isIgnored(path string, isDirectory bool, rules []ignoreRule) bool {
	ignored := false
	for _, rule := range rules {
		if rule.matches(path, isDirectory) {
			ignored = !rule.negated
		}
	}
	return ignored
}

func (rule ignoreRule) matches(path string, isDirectory bool) bool {
	relative, err := filepath.Rel(rule.base, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	prefix := ""
	for index, part := range parts {
		if prefix == "" {
			prefix = part
		} else {
			prefix += "/" + part
		}
		isTargetDirectory := index < len(parts)-1 || isDirectory
		if rule.directory && !isTargetDirectory {
			continue
		}
		if rule.anchored || strings.Contains(rule.pattern, "/") {
			if globMatches(rule.pattern, prefix) {
				return true
			}
		} else if globMatches(rule.pattern, part) {
			return true
		}
	}
	return false
}

func globMatches(pattern, value string) bool {
	type position struct{ pattern, value int }
	memo := make(map[position]bool)
	seen := make(map[position]bool)
	var match func(int, int) bool
	match = func(patternOffset, valueOffset int) bool {
		key := position{pattern: patternOffset, value: valueOffset}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true
		var result bool
		switch {
		case patternOffset == len(pattern):
			result = valueOffset == len(value)
		case pattern[patternOffset] == '\\' && patternOffset+1 < len(pattern):
			result = valueOffset < len(value) && pattern[patternOffset+1] == value[valueOffset] && match(patternOffset+2, valueOffset+1)
		case pattern[patternOffset] == '*':
			if patternOffset+1 < len(pattern) && pattern[patternOffset+1] == '*' {
				next := patternOffset + 2
				for next < len(pattern) && pattern[next] == '*' {
					next++
				}
				if next < len(pattern) && pattern[next] == '/' {
					result = match(next+1, valueOffset)
					for index := valueOffset; !result && index < len(value); index++ {
						if value[index] == '/' {
							result = match(next+1, index+1)
						}
					}
				} else {
					for index := valueOffset; !result && index <= len(value); index++ {
						result = match(next, index)
					}
				}
			} else {
				result = match(patternOffset+1, valueOffset)
				for index := valueOffset; !result && index < len(value) && value[index] != '/'; index++ {
					result = match(patternOffset+1, index+1)
				}
			}
		case pattern[patternOffset] == '?':
			result = valueOffset < len(value) && value[valueOffset] != '/' && match(patternOffset+1, valueOffset+1)
		case pattern[patternOffset] == '[':
			end, matched := matchCharacterClass(pattern, value, patternOffset, valueOffset)
			if end >= 0 {
				result = matched && match(end+1, valueOffset+1)
			} else {
				result = valueOffset < len(value) && value[valueOffset] == '[' && match(patternOffset+1, valueOffset+1)
			}
		default:
			result = valueOffset < len(value) && pattern[patternOffset] == value[valueOffset] && match(patternOffset+1, valueOffset+1)
		}
		memo[key] = result
		return result
	}
	return match(0, 0)
}

func matchCharacterClass(pattern, value string, patternOffset, valueOffset int) (int, bool) {
	if valueOffset >= len(value) || value[valueOffset] == '/' {
		return -1, false
	}
	end := patternOffset + 1
	for end < len(pattern) {
		if pattern[end] == '\\' {
			end += 2
			continue
		}
		if pattern[end] == ']' {
			break
		}
		end++
	}
	if end >= len(pattern) {
		return -1, false
	}
	class := pattern[patternOffset+1 : end]
	negated := strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^")
	if negated {
		class = class[1:]
	}
	matched := false
	for index := 0; index < len(class); index++ {
		first := class[index]
		if first == '\\' && index+1 < len(class) {
			index++
			first = class[index]
		}
		if index+2 < len(class) && class[index+1] == '-' {
			last := class[index+2]
			if value[valueOffset] >= first && value[valueOffset] <= last {
				matched = true
			}
			index += 2
			continue
		}
		if value[valueOffset] == first {
			matched = true
		}
	}
	if negated {
		matched = !matched
	}
	return end, matched
}
