package requestlog

import (
	"net/http"
	"testing"
)

func TestMatcherSemantics(t *testing.T) {
	matcher, err := CompileMatcher([]FilterConfig{
		{Principals: []string{"^team-a$"}, Models: []string{"^Qwen/"}, Headers: map[string][]string{"X-Session": {"^job-", "^run-"}}},
		{Models: []string{"^public$"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		principal string
		model     string
		values    []string
		want      bool
	}{
		{name: "all constraints", principal: "team-a", model: "Qwen/32B", values: []string{"unmatched", "job-42"}, want: true},
		{name: "principal mismatch", principal: "team-b", model: "Qwen/32B", values: []string{"job-42"}},
		{name: "header mismatch", principal: "team-a", model: "Qwen/32B", values: []string{"other"}},
		{name: "second OR rule", principal: "any", model: "public", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			for _, value := range test.values {
				headers.Add("X-Session", value)
			}
			if got := matcher.Match(test.principal, test.model, headers); got != test.want {
				t.Fatalf("Match() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMatcherEmptyAndCaptureAll(t *testing.T) {
	deny, err := CompileMatcher(nil)
	if err != nil {
		t.Fatal(err)
	}
	if deny.Match("ANONYMOUS", "model", nil) {
		t.Fatal("no rules must deny")
	}
	all, err := CompileMatcher([]FilterConfig{{}})
	if err != nil {
		t.Fatal(err)
	}
	if !all.Match("ANONYMOUS", "model", nil) {
		t.Fatal("empty rule must capture all accepted requests")
	}
}

func TestMatcherRequestHost(t *testing.T) {
	actual, err := CompileMatcher([]FilterConfig{{Headers: map[string][]string{"hOsT": {`^borg\.internal:8443$`}}}})
	if err != nil {
		t.Fatal(err)
	}
	synthetic, err := CompileMatcher([]FilterConfig{{Headers: map[string][]string{"Host": {`^synthetic\.invalid$`}}}})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Host": {"synthetic.invalid"}}
	if !actual.MatchRequest("ANONYMOUS", "model", headers, "borg.internal:8443") {
		t.Fatal("dedicated request Host did not match case-insensitively")
	}
	if synthetic.MatchRequest("ANONYMOUS", "model", headers, "borg.internal:8443") {
		t.Fatal("synthetic Host map entry overrode the dedicated request Host")
	}
	if actual.MatchRequest("ANONYMOUS", "model", headers, "") || synthetic.MatchRequest("ANONYMOUS", "model", headers, "") {
		t.Fatal("an empty dedicated request Host must not match a synthetic map entry")
	}
}
