package store

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ── Discussions ──

func (s *PostgresStore) CreateThread(ctx context.Context, thread *domain.DiscussionThread) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.discussion_threads (thread_id, anchor_type, anchor_id, topic_key, title, status, created_by, created_at, resolved_at, resolution_type, resolution_refs)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		thread.ThreadID, thread.AnchorType, thread.AnchorID,
		nilIfEmpty(thread.TopicKey), nilIfEmpty(thread.Title),
		thread.Status, thread.CreatedBy, thread.CreatedAt,
		thread.ResolvedAt, nilIfEmpty(string(thread.ResolutionType)), thread.ResolutionRefs,
	)
	return err
}

func (s *PostgresStore) GetThread(ctx context.Context, threadID string) (*domain.DiscussionThread, error) {
	var t domain.DiscussionThread
	var topicKey, title, resolutionType *string
	err := s.pool.QueryRow(ctx, `
		SELECT thread_id, anchor_type, anchor_id, topic_key, title, status, created_by, created_at, resolved_at, resolution_type, resolution_refs
		FROM runtime.discussion_threads WHERE thread_id = $1`, threadID,
	).Scan(
		&t.ThreadID, &t.AnchorType, &t.AnchorID, &topicKey, &title,
		&t.Status, &t.CreatedBy, &t.CreatedAt, &t.ResolvedAt,
		&resolutionType, &t.ResolutionRefs,
	)
	if err != nil {
		return nil, notFoundOr(err, "thread not found")
	}
	if topicKey != nil {
		t.TopicKey = *topicKey
	}
	if title != nil {
		t.Title = *title
	}
	if resolutionType != nil {
		t.ResolutionType = domain.ResolutionType(*resolutionType)
	}
	return &t, nil
}

func (s *PostgresStore) ListThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) ([]domain.DiscussionThread, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT thread_id, anchor_type, anchor_id, topic_key, title, status, created_by, created_at, resolved_at, resolution_type, resolution_refs
		FROM runtime.discussion_threads WHERE anchor_type = $1 AND anchor_id = $2
		ORDER BY created_at DESC`, anchorType, anchorID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []domain.DiscussionThread
	for rows.Next() {
		var t domain.DiscussionThread
		var topicKey, title, resolutionType *string
		if err := rows.Scan(
			&t.ThreadID, &t.AnchorType, &t.AnchorID, &topicKey, &title,
			&t.Status, &t.CreatedBy, &t.CreatedAt, &t.ResolvedAt,
			&resolutionType, &t.ResolutionRefs,
		); err != nil {
			return nil, err
		}
		if topicKey != nil {
			t.TopicKey = *topicKey
		}
		if title != nil {
			t.Title = *title
		}
		if resolutionType != nil {
			t.ResolutionType = domain.ResolutionType(*resolutionType)
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

func (s *PostgresStore) UpdateThread(ctx context.Context, thread *domain.DiscussionThread) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.discussion_threads
		SET status = $1, title = $2, resolved_at = $3, resolution_type = $4, resolution_refs = $5
		WHERE thread_id = $6`,
		thread.Status, nilIfEmpty(thread.Title), thread.ResolvedAt,
		nilIfEmpty(string(thread.ResolutionType)), thread.ResolutionRefs, thread.ThreadID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "thread not found")
}

func (s *PostgresStore) CreateComment(ctx context.Context, comment *domain.Comment) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.comments (comment_id, thread_id, parent_comment_id, author_id, author_type, content, metadata, created_at, edited_at, deleted)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		comment.CommentID, comment.ThreadID, nilIfEmpty(comment.ParentCommentID),
		comment.AuthorID, comment.AuthorType, comment.Content, comment.Metadata,
		comment.CreatedAt, comment.EditedAt, comment.Deleted,
	)
	return err
}

func (s *PostgresStore) ListComments(ctx context.Context, threadID string) ([]domain.Comment, error) {
	return queryAll(ctx, s.pool, `
		SELECT comment_id, thread_id, parent_comment_id, author_id, author_type, content, metadata, created_at, edited_at, deleted
		FROM runtime.comments WHERE thread_id = $1
		ORDER BY created_at ASC`,
		[]any{threadID},
		func(row pgx.Rows, c *domain.Comment) error {
			var parentCommentID *string
			if err := row.Scan(
				&c.CommentID, &c.ThreadID, &parentCommentID,
				&c.AuthorID, &c.AuthorType, &c.Content, &c.Metadata,
				&c.CreatedAt, &c.EditedAt, &c.Deleted,
			); err != nil {
				return err
			}
			if parentCommentID != nil {
				c.ParentCommentID = *parentCommentID
			}
			return nil
		},
	)
}

func (s *PostgresStore) HasOpenThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM runtime.discussion_threads
		WHERE anchor_type = $1 AND anchor_id = $2 AND status = 'open'`,
		anchorType, anchorID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
