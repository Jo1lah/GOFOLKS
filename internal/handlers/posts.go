package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gofolks/internal/middleware"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostHandler struct {
	DB *pgxpool.Pool
}

type Post struct {
	ID          int64      `json:"id"`
	UserID      int64      `json:"user_id"`
	Username    string     `json:"username,omitempty"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Location    *string    `json:"location"`
	EventDate   *time.Time `json:"event_date"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type postRequest struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Location    *string    `json:"location"`
	EventDate   *time.Time `json:"event_date"`
}

func (h *PostHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.DB.Query(ctx, `
		SELECT p.id, p.user_id, u.username, p.title, p.description,
		       p.location, p.event_date, p.created_at, p.updated_at
		FROM posts p
		JOIN users u ON u.id = p.user_id
		ORDER BY p.created_at DESC
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not load posts",
		})
		return
	}
	defer rows.Close()

	posts := make([]Post, 0)
	for rows.Next() {
		var p Post
		if err := rows.Scan(
			&p.ID, &p.UserID, &p.Username, &p.Title, &p.Description,
			&p.Location, &p.EventDate, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Could not read posts",
			})
			return
		}

		posts = append(posts, p)
	}

	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not read posts",
		})
		return
	}

	writeJSON(w, http.StatusOK, posts)
}

func (h *PostHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := postID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var p Post
	err := h.DB.QueryRow(ctx, `
		SELECT p.id, p.user_id, u.username, p.title, p.description,
		       p.location, p.event_date, p.created_at, p.updated_at
		FROM posts p
		JOIN users u ON u.id = p.user_id
		WHERE p.id = $1
	`, id).Scan(
		&p.ID, &p.UserID, &p.Username, &p.Title, &p.Description,
		&p.Location, &p.EventDate, &p.CreatedAt, &p.UpdatedAt,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Post not found",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not load post",
		})
		return
	}

	writeJSON(w, http.StatusOK, p)
}

func (h *PostHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}

	var input postRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON body",
		})
		return
	}

	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)

	if input.Title == "" || len(input.Title) > 150 || input.Description == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Title is required (max 150 characters) and description is required",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var p Post
	err := h.DB.QueryRow(ctx, `
		INSERT INTO posts (user_id, title, description, location, event_date)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, title, description, location, event_date, created_at, updated_at
	`, userID, input.Title, input.Description, input.Location, input.EventDate).Scan(
		&p.ID, &p.UserID, &p.Title, &p.Description,
		&p.Location, &p.EventDate, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not create post",
		})
		return
	}

	w.Header().Set("Location", "/api/posts/"+strconv.FormatInt(p.ID, 10))
	writeJSON(w, http.StatusCreated, p)
}

func (h *PostHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := postID(w, r)
	if !ok {
		return
	}

	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}

	var input postRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON body",
		})
		return
	}

	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)

	if input.Title == "" || len(input.Title) > 150 || input.Description == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Title is required (max 150 characters) and description is required",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var p Post
	err := h.DB.QueryRow(ctx, `
		UPDATE posts
		SET title = $1, description = $2, location = $3,
		    event_date = $4, updated_at = NOW()
		WHERE id = $5 AND user_id = $6
		RETURNING id, user_id, title, description, location, event_date, created_at, updated_at
	`, input.Title, input.Description, input.Location, input.EventDate, id, userID).Scan(
		&p.ID, &p.UserID, &p.Title, &p.Description,
		&p.Location, &p.EventDate, &p.CreatedAt, &p.UpdatedAt,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		h.writeMissingOrForbidden(w, r, id, userID)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not update post",
		})
		return
	}

	writeJSON(w, http.StatusOK, p)
}

func (h *PostHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := postID(w, r)
	if !ok {
		return
	}

	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.DB.Exec(ctx,
		`DELETE FROM posts WHERE id = $1 AND user_id = $2`,
		id, userID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not delete post",
		})
		return
	}

	if tag.RowsAffected() == 0 {
		h.writeMissingOrForbidden(w, r, id, userID)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *PostHandler) writeMissingOrForbidden(
	w http.ResponseWriter,
	r *http.Request,
	id, userID int64,
) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var exists bool
	err := h.DB.QueryRow(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM posts WHERE id = $1)`,
		id,
	).Scan(&exists)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not verify post",
		})
		return
	}

	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Post not found",
		})
		return
	}

	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "You can only modify your own posts",
	})
}

func postID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := mux.Vars(r)["id"]
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid post ID",
		})
		return 0, false
	}

	return id, true
}

func currentUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := r.Context().Value(middleware.UserIDKey).(int64)
	if !ok || id <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Unauthorized",
		})
		return 0, false
	}

	return id, true
}
