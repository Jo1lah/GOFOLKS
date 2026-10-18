package models

import "time"

type Activity struct {
	ID              int64      `json:"id"`
	CreatorID       int64      `json:"creator_id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Location        string     `json:"location"`
	ActivityDate    *time.Time `json:"activity_date"`
	MaxParticipants int        `json:"max_participants"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type CreateActivityRequest struct {
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Location        string     `json:"location"`
	ActivityDate    *time.Time `json:"activity_date"`
	MaxParticipants int        `json:"max_participants"`
}

type ActivityParticipant struct {
	ActivityID int64     `json:"activity_id"`
	UserID     int64     `json:"user_id"`
	JoinedAt   time.Time `json:"joined_at"`
}
