package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type Message struct {
	ID        bson.ObjectID `bson:"_id,omitempty" json:"id"`
	ChatID    string        `bson:"chat_id" json:"chat_id"`
	SenderID  int64         `bson:"sender_id" json:"sender_id"`
	Content   string        `bson:"content" json:"content"`
	CreatedAt time.Time     `bson:"created_at" json:"created_at"`
}
