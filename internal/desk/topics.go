package desk

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) Topics(project, query, follower string) []Topic {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Topic{}
	for _, t := range m.state.Topics {
		if t.ProjectID != project || !strings.Contains(strings.ToLower(t.Title+" "+t.Summary+" "+strings.Join(t.Paths, " ")), strings.ToLower(query)) {
			continue
		}
		if follower != "" {
			if _, ok := t.Followers[follower]; !ok {
				continue
			}
		}
		t.Posts = nil
		if follower != "" {
			t.Followers = map[string]int{follower: t.Followers[follower]}
		} else {
			t.Followers = nil
		}
		result = append(result, t)
	}
	return result
}
func (m *Manager) topicLocked(id string) (*Topic, error) {
	for i := range m.state.Topics {
		if m.state.Topics[i].ID == id {
			return &m.state.Topics[i], nil
		}
	}
	return nil, ErrNotFound
}
func (m *Manager) Topic(id string, after int) (Topic, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.topicLocked(id)
	if err != nil {
		return Topic{}, err
	}
	if after < 0 || after > t.Revision {
		return Topic{}, errors.New("invalid topic cursor")
	}
	out := *t
	out.Followers = nil
	out.Paths = append([]string{}, t.Paths...)
	out.Posts = []Post{}
	for _, p := range t.Posts {
		if p.Sequence > after {
			out.Posts = append(out.Posts, p)
			if len(out.Posts) == 50 {
				break
			}
		}
	}
	return out, nil
}
func (m *Manager) CreateTopic(project, author, title, summary string, paths []string) (Topic, error) {
	if !validText(title, 160) || !validText(summary, 1000) || len(paths) > 32 {
		return Topic{}, errors.New("provide a title (160 characters), summary (1000 characters), and at most 32 paths")
	}
	for _, p := range paths {
		if !filepath.IsLocal(p) {
			return Topic{}, errors.New("topic paths must be relative")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return Topic{}, err
	}
	if _, err := m.projectLocked(project); err != nil {
		return Topic{}, err
	}
	if err := m.authorLocked(project, author); err != nil {
		return Topic{}, err
	}
	if len(m.state.Topics) >= 2048 {
		return Topic{}, errors.New("topic limit reached")
	}
	now := time.Now().UTC()
	t := Topic{ID: id("topic"), ProjectID: project, Title: title, Summary: summary, Paths: paths, Revision: 1, UpdatedAt: now, Posts: []Post{{Sequence: 1, Author: author, Body: summary, At: now}}, Followers: map[string]int{}}
	m.state.Topics = append(m.state.Topics, t)
	err := m.saveLocked()
	t.Followers = nil
	return t, err
}
func (m *Manager) Post(id, author, body string, expected int) (Post, error) {
	if !validText(body, 12000) {
		return Post{}, errors.New("post must be 1–12000 bytes")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return Post{}, err
	}
	t, err := m.topicLocked(id)
	if err != nil {
		return Post{}, err
	}
	if err = m.authorLocked(t.ProjectID, author); err != nil {
		return Post{}, err
	}
	if expected != t.Revision {
		return Post{}, ErrConflict
	}
	if len(t.Posts) >= 1024 {
		return Post{}, errors.New("topic post limit reached; create a follow-up topic")
	}
	p := Post{Sequence: t.Revision + 1, Author: author, Body: body, At: time.Now().UTC()}
	t.Posts = append(t.Posts, p)
	t.Revision = p.Sequence
	t.UpdatedAt = p.At
	return p, m.saveLocked()
}
func (m *Manager) Follow(id, session string, cursor int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	t, err := m.topicLocked(id)
	if err != nil {
		return err
	}
	if err = m.authorLocked(t.ProjectID, session); err != nil {
		return err
	}
	if cursor < 0 || cursor > t.Revision {
		return errors.New("invalid acknowledgment cursor")
	}
	if t.Followers == nil {
		t.Followers = map[string]int{}
	}
	if cursor < t.Followers[session] {
		return ErrConflict
	}
	t.Followers[session] = cursor
	return m.saveLocked()
}
