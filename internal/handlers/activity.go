package handlers

import (
	"context"

	"net/http"
	"strconv"
	"strings"
	"time"

	"gofolks/internal/models"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ActivityHandler struct {
	DB *pgxpool.Pool
}

func (h *ActivityHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.DB.Query(ctx, `
		SELECT a.id, a.creator_id, a.title, a.description,
		       a.location, a.activity_date, a.max_participants,
		       a.status, a.created_at, a.updated_at
		FROM activities a
		WHERE a.status IN ('open', 'full')
		ORDER BY a.created_at DESC
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not load activities",
		})
		return
	}
	defer rows.Close()

	activities := make([]models.Activity, 0)
	for rows.Next() {
		var a models.Activity
		if err := rows.Scan(
			&a.ID, &a.CreatorID, &a.Title, &a.Description,
			&a.Location, &a.ActivityDate, &a.MaxParticipants,
			&a.Status, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Could not read activities",
			})
			return
		}
		activities = append(activities, a)
	}

	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not read activities",
		})
		return
	}

	writeJSON(w, http.StatusOK, activities)
}

func (h *ActivityHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}

	var input models.CreateActivityRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON body",
		})
		return
	}

	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	input.Location = strings.TrimSpace(input.Location)

	if input.Title == "" || len(input.Title) > 150 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Title is required and must be at most 150 characters",
		})
		return
	}

	if input.MaxParticipants == 0 {
		input.MaxParticipants = 2
	}
	if input.MaxParticipants < 2 || input.MaxParticipants > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "max_participants must be between 2 and 1000",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not start transaction",
		})
		return
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var activity models.Activity
	err = tx.QueryRow(ctx, `
		INSERT INTO activities (
			creator_id, title, description, location,
			activity_date, max_participants
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, creator_id, title, description, location,
		          activity_date, max_participants, status,
		          created_at, updated_at
	`, userID, input.Title, input.Description, input.Location,
		input.ActivityDate, input.MaxParticipants).Scan(
		&activity.ID, &activity.CreatorID, &activity.Title,
		&activity.Description, &activity.Location, &activity.ActivityDate,
		&activity.MaxParticipants, &activity.Status,
		&activity.CreatedAt, &activity.UpdatedAt,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not create activity",
		})
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO activity_participants (activity_id, user_id)
		VALUES ($1, $2)
	`, activity.ID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not add activity creator as participant",
		})
		return
	}

	// Для каждой активности создаём отдельный чат.
	chatID := "activity-" + strconv.FormatInt(activity.ID, 10)
	_, err = tx.Exec(ctx, `
		INSERT INTO chats (id, created_by, activity_id)
		VALUES ($1, $2, $3)
	`, chatID, userID, activity.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not create activity chat",
		})
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO chat_members (chat_id, user_id)
		VALUES ($1, $2)
	`, chatID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not add creator to activity chat",
		})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not save activity",
		})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"activity": activity,
		"chat_id":  chatID,
	})
}

func (h *ActivityHandler) Join(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}

	rawID := mux.Vars(r)["id"]
	activityID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || activityID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid activity ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not start transaction",
		})
		return
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var chatID string
	var status string
	var maxParticipants int
	var participantCount int

	err = tx.QueryRow(ctx, `
		SELECT c.id, a.status, a.max_participants,
		       (SELECT COUNT(*) FROM activity_participants ap
		        WHERE ap.activity_id = a.id)
		FROM activities a
		JOIN chats c ON c.activity_id = a.id
		WHERE a.id = $1
		FOR UPDATE OF a
	`, activityID).Scan(&chatID, &status, &maxParticipants, &participantCount)

	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Activity not found",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not load activity",
		})
		return
	}

	if status != "open" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Activity is not open for joining",
		})
		return
	}

	var alreadyMember bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM activity_participants
			WHERE activity_id = $1 AND user_id = $2
		)
	`, activityID, userID).Scan(&alreadyMember)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not check participation",
		})
		return
	}
	if alreadyMember {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Already joined this activity",
		})
		return
	}

	if participantCount >= maxParticipants {
		_, _ = tx.Exec(ctx, `
			UPDATE activities SET status = 'full', updated_at = NOW()
			WHERE id = $1
		`, activityID)
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Activity is full",
		})
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO activity_participants (activity_id, user_id)
		VALUES ($1, $2)
	`, activityID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not join activity",
		})
		return
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO chat_members (chat_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT (chat_id, user_id) DO NOTHING
	`, chatID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not grant activity chat access",
		})
		return
	}

	if participantCount+1 >= maxParticipants {
		_, err = tx.Exec(ctx, `
			UPDATE activities SET status = 'full', updated_at = NOW()
			WHERE id = $1
		`, activityID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Could not update activity status",
			})
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not save participation",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message":     "Joined activity successfully",
		"chat_id":     chatID,
		"activity_id": strconv.FormatInt(activityID, 10),
	})
}

func (h *ActivityHandler) MyActivities(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.DB.Query(ctx, `
		SELECT a.id, a.creator_id, a.title, a.description,
		       a.location, a.activity_date, a.max_participants,
		       a.status, a.created_at, a.updated_at
		FROM activities a
		JOIN activity_participants ap ON ap.activity_id = a.id
		WHERE ap.user_id = $1
		ORDER BY a.created_at DESC
	`, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not load your activities",
		})
		return
	}
	defer rows.Close()

	activities := make([]models.Activity, 0)

	for rows.Next() {
		var a models.Activity

		if err := rows.Scan(
			&a.ID, &a.CreatorID, &a.Title, &a.Description,
			&a.Location, &a.ActivityDate, &a.MaxParticipants,
			&a.Status, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Could not read your activities",
			})
			return
		}

		activities = append(activities, a)
	}

	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not read your activities",
		})
		return
	}

	writeJSON(w, http.StatusOK, activities)
}

func (h *ActivityHandler) Leave(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}

	rawID := mux.Vars(r)["id"]
	activityID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || activityID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid activity ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not start transaction",
		})
		return
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var creatorID int64
	var chatID string
	var status string

	err = tx.QueryRow(ctx, `
		SELECT a.creator_id, c.id, a.status
		FROM activities a
		JOIN chats c ON c.activity_id = a.id
		WHERE a.id = $1
		FOR UPDATE OF a
	`, activityID).Scan(&creatorID, &chatID, &status)

	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Activity not found",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not load activity",
		})
		return
	}

	if creatorID == userID {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Organizer cannot leave their own activity",
		})
		return
	}

	result, err := tx.Exec(ctx, `
		DELETE FROM activity_participants
		WHERE activity_id = $1 AND user_id = $2
	`, activityID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not leave activity",
		})
		return
	}
	if result.RowsAffected() == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "You have not joined this activity",
		})
		return
	}

	_, err = tx.Exec(ctx, `
		DELETE FROM chat_members
		WHERE chat_id = $1 AND user_id = $2
	`, chatID, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not remove chat access",
		})
		return
	}

	if status == "full" {
		_, err = tx.Exec(ctx, `
			UPDATE activities
			SET status = 'open', updated_at = NOW()
			WHERE id = $1
		`, activityID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Could not reopen activity",
			})
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not save changes",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message":     "Left activity successfully",
		"activity_id": activityID,
	})
}
