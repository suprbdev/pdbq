package subscribe

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxChannelBytes is the longest PostgreSQL channel name (NAMEDATALEN - 1).
// LISTEN silently truncates a longer identifier while pg_notify rejects it,
// so a longer channel could never receive anything.
const MaxChannelBytes = 63

var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/-]+$`)

// TopicPolicy maps listen topics to PostgreSQL channels: the channel is the
// prefix followed by the topic. A topic must consist of letters, digits and
// the characters _ . : / -, must be allowed by the allowlist when there is
// one, and must leave the channel within MaxChannelBytes.
type TopicPolicy struct {
	prefix   string
	exact    map[string]bool
	prefixes []string
	any      bool
}

// NewTopicPolicy builds the policy for a channel prefix and a topic
// allowlist. An allowlist entry is a topic, or a topic prefix followed by
// "*"; a lone "*" or an empty allowlist allows every valid topic. The error
// names the offending setting (subscriptions_topic_prefix or
// subscriptions_topics).
func NewTopicPolicy(prefix string, allow []string) (*TopicPolicy, error) {
	if strings.ContainsRune(prefix, 0) || len(prefix) >= MaxChannelBytes {
		return nil, fmt.Errorf("subscriptions_topic_prefix: %q must be shorter than %d bytes and contain no NUL", prefix, MaxChannelBytes)
	}
	p := &TopicPolicy{prefix: prefix, exact: map[string]bool{}, any: len(allow) == 0}
	for _, t := range allow {
		switch base, wild := strings.CutSuffix(t, "*"); {
		case t == "*":
			p.any = true
		case !topicPattern.MatchString(base):
			return nil, fmt.Errorf("subscriptions_topics: %q is not a valid topic (letters, digits and _ . : / -, optionally ending in *)", t)
		case wild:
			p.prefixes = append(p.prefixes, base)
		default:
			p.exact[t] = true
		}
	}
	return p, nil
}

// Channel returns the PostgreSQL channel for topic, or an error a client
// may see when the topic is malformed, not allowed or too long.
func (p *TopicPolicy) Channel(topic string) (string, error) {
	if !topicPattern.MatchString(topic) {
		return "", fmt.Errorf("invalid topic %q: use letters, digits and _ . : / -", topic)
	}
	if !p.allowed(topic) {
		return "", fmt.Errorf("topic %q is not allowed", topic)
	}
	ch := p.prefix + topic
	if len(ch) > MaxChannelBytes {
		return "", fmt.Errorf("topic %q is too long: the channel %q exceeds %d bytes", topic, ch, MaxChannelBytes)
	}
	return ch, nil
}

func (p *TopicPolicy) allowed(topic string) bool {
	if p.any || p.exact[topic] {
		return true
	}
	for _, pre := range p.prefixes {
		if strings.HasPrefix(topic, pre) {
			return true
		}
	}
	return false
}
