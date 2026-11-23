package subscribe

import (
	"strings"
	"testing"
)

func TestTopicPolicy(t *testing.T) {
	open, err := NewTopicPolicy("postgraphile:", nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := NewTopicPolicy("app:", []string{"chat", "user:*"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		policy  *TopicPolicy
		topic   string
		channel string
		err     string
	}{
		{open, "hello", "postgraphile:hello", ""},
		{open, "a.b:c/d-e_f", "postgraphile:a.b:c/d-e_f", ""},
		{open, "", "", "invalid topic"},
		{open, `x"; DROP TABLE users; --`, "", "invalid topic"},
		{open, "héllo", "", "invalid topic"},
		{open, strings.Repeat("a", 51), "", "too long"},
		{open, strings.Repeat("a", 50), "postgraphile:" + strings.Repeat("a", 50), ""},
		{listed, "chat", "app:chat", ""},
		{listed, "chats", "", "not allowed"},
		{listed, "user:1", "app:user:1", ""},
		{listed, "user", "", "not allowed"},
	} {
		ch, err := tc.policy.Channel(tc.topic)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("Channel(%q) error = %v, want %q", tc.topic, err, tc.err)
			}
			continue
		}
		if err != nil || ch != tc.channel {
			t.Errorf("Channel(%q) = %q, %v; want %q", tc.topic, ch, err, tc.channel)
		}
	}
}

func TestNewTopicPolicyErrors(t *testing.T) {
	if _, err := NewTopicPolicy(strings.Repeat("p", MaxChannelBytes), nil); err == nil || !strings.Contains(err.Error(), "topic_prefix") {
		t.Errorf("long prefix: %v", err)
	}
	if _, err := NewTopicPolicy("p\x00", nil); err == nil {
		t.Error("NUL prefix accepted")
	}
	if _, err := NewTopicPolicy("p", []string{"ok", "bad topic"}); err == nil || !strings.Contains(err.Error(), "topics:") {
		t.Errorf("bad allowlist entry: %v", err)
	}
	p, err := NewTopicPolicy("", []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Channel("anything"); err != nil {
		t.Errorf("* allowlist: %v", err)
	}
}
