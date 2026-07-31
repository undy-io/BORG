package requestlog

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

type Matcher struct {
	rules []compiledRule
}

type compiledRule struct {
	principals []*regexp.Regexp
	models     []*regexp.Regexp
	headers    map[string][]*regexp.Regexp
}

func CompileMatcher(filters []FilterConfig) (*Matcher, error) {
	matcher := &Matcher{rules: make([]compiledRule, 0, len(filters))}
	for idx, filter := range filters {
		if filter.Principals != nil && len(filter.Principals) == 0 {
			return nil, fmt.Errorf("request_logging filter %d principals cannot be an empty list", idx)
		}
		if filter.Models != nil && len(filter.Models) == 0 {
			return nil, fmt.Errorf("request_logging filter %d models cannot be an empty list", idx)
		}
		rule := compiledRule{headers: make(map[string][]*regexp.Regexp, len(filter.Headers))}
		var err error
		rule.principals, err = compilePatterns(filter.Principals, fmt.Sprintf("filter %d principals", idx))
		if err != nil {
			return nil, err
		}
		rule.models, err = compilePatterns(filter.Models, fmt.Sprintf("filter %d models", idx))
		if err != nil {
			return nil, err
		}
		for rawName, patterns := range filter.Headers {
			if !validHeaderName(rawName) {
				return nil, fmt.Errorf("request_logging filter %d header %q is invalid", idx, rawName)
			}
			name := strings.ToLower(rawName)
			if len(patterns) == 0 {
				return nil, fmt.Errorf("request_logging filter %d header %q patterns cannot be empty", idx, rawName)
			}
			if _, duplicate := rule.headers[name]; duplicate {
				return nil, fmt.Errorf("request_logging filter %d repeats header %q with different casing", idx, rawName)
			}
			rule.headers[name], err = compilePatterns(patterns, fmt.Sprintf("filter %d header %q", idx, rawName))
			if err != nil {
				return nil, err
			}
		}
		matcher.rules = append(matcher.rules, rule)
	}
	return matcher, nil
}

func compilePatterns(patterns []string, field string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("request_logging %s pattern %q is invalid: %w", field, pattern, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

func (m *Matcher) Match(principal string, model string, headers http.Header) bool {
	return m.match(principal, model, plainHeaderView(headers))
}

func (m *Matcher) MatchRequest(principal string, model string, headers http.Header, host string) bool {
	return m.match(principal, model, requestHeaderView(headers, host))
}

func (m *Matcher) match(principal string, model string, headers headerView) bool {
	if m == nil {
		return false
	}
	for _, rule := range m.rules {
		if !matchesAny(rule.principals, principal) || !matchesAny(rule.models, model) {
			continue
		}
		matched := true
		for name, patterns := range rule.headers {
			if !matchesHeader(patterns, headers, name) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func matchesHeader(patterns []*regexp.Regexp, headers headerView, name string) bool {
	if headers.dedicatedHost && strings.EqualFold(name, "host") {
		return headers.host != "" && matchesAny(patterns, headers.host)
	}
	return matchesHeaderValues(patterns, headers.Values(name))
}

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func matchesHeaderValues(patterns []*regexp.Regexp, values []string) bool {
	for _, value := range values {
		for _, pattern := range patterns {
			if pattern.MatchString(value) {
				return true
			}
		}
	}
	return false
}
