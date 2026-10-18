package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"gofolks/internal/middleware"
	"gofolks/internal/models"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ChatHandler struct {
	Collection *mongo.Collection
	DB         *pgxpool.Pool
}

type ChatMessageRequest struct {
	Content string `json:"content"`
}

type chatClient struct {
	conn   *websocket.Conn
	chatID string
	userID int64
	send   chan models.Message
}

type chatHub struct {
	mu      sync.Mutex
	clients map[*chatClient]bool
}

var hub = chatHub{
	clients: make(map[*chatClient]bool),
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// TODO: ограничить разрешённые Origin перед production.
		return true
	},
}

func (h *ChatHandler) isChatMember(
	ctx context.Context,
	chatID string,
	userID int64,
) (bool, error) {
	var exists bool

	err := h.DB.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM chats c
			JOIN activity_participants ap
				ON ap.activity_id = c.activity_id
			WHERE c.id = $1
				AND ap.user_id = $2
				AND c.activity_id IS NOT NULL
		)
	`, chatID, userID).Scan(&exists)

	return exists, err
}

func (h *ChatHandler) WebSocket(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	userID, ok := middleware.ConsumeWebSocketTicket(ticket)
	if !ok || userID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	chatID := strings.TrimSpace(r.URL.Query().Get("chat_id"))
	if chatID == "" || len(chatID) > 100 {
		http.Error(w, "chat_id is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	allowed, err := h.isChatMember(ctx, chatID, userID)
	cancel()

	if err != nil {
		log.Printf("Chat membership check failed: %v", err)
		http.Error(w, "Could not verify chat access", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "You are not a member of this chat", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	client := &chatClient{
		conn:   conn,
		chatID: chatID,
		userID: userID,
		send:   make(chan models.Message, 32),
	}

	hub.mu.Lock()
	hub.clients[client] = true
	hub.mu.Unlock()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)

		for message := range client.send {
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteJSON(message); err != nil {
				return
			}
		}
	}()

	defer func() {
		hub.mu.Lock()
		if _, exists := hub.clients[client]; exists {
			delete(hub.clients, client)
			close(client.send)
		}
		hub.mu.Unlock()

		_ = conn.Close()
		<-writerDone
	}()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))

		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var request ChatMessageRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			client.sendError("Invalid JSON")
			continue
		}

		content := strings.TrimSpace(request.Content)
		if content == "" || len(content) > 5000 {
			client.sendError("Message must contain 1-5000 characters")
			continue
		}

		message := models.Message{
			ChatID:    chatID,
			SenderID:  userID,
			Content:   content,
			CreatedAt: time.Now().UTC(),
		}

		saveCtx, saveCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		result, err := h.Collection.InsertOne(saveCtx, message)
		saveCancel()

		if err != nil {
			log.Printf("Save chat message failed: %v", err)
			client.sendError("Could not save message")
			continue
		}

		if id, ok := result.InsertedID.(bson.ObjectID); ok {
			message.ID = id
		}

		h.broadcast(chatID, message)
	}
}

func (c *chatClient) sendError(message string) {
	select {
	case c.send <- models.Message{
		ChatID:    c.chatID,
		SenderID:  c.userID,
		Content:   "ERROR: " + message,
		CreatedAt: time.Now().UTC(),
	}:
	default:
	}
}

func (h *ChatHandler) broadcast(chatID string, message models.Message) {
	hub.mu.Lock()
	defer hub.mu.Unlock()

	for client := range hub.clients {
		if client.chatID != chatID {
			continue
		}

		select {
		case client.send <- message:
		default:
			delete(hub.clients, client)
			close(client.send)
			_ = client.conn.Close()
		}
	}
}

func (h *ChatHandler) History(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int64)
	if !ok || userID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	chatID := strings.TrimSpace(r.URL.Query().Get("chat_id"))
	if chatID == "" || len(chatID) > 100 {
		http.Error(w, "chat_id is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	allowed, err := h.isChatMember(ctx, chatID, userID)
	if err != nil {
		log.Printf("Chat membership check failed: %v", err)
		http.Error(w, "Could not verify chat access", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "You are not a member of this chat", http.StatusForbidden)
		return
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(50)

	cursor, err := h.Collection.Find(ctx, bson.M{"chat_id": chatID}, opts)
	if err != nil {
		log.Printf("Load chat messages failed: %v", err)
		http.Error(w, "Could not load messages", http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	messages := make([]models.Message, 0)
	if err := cursor.All(ctx, &messages); err != nil {
		log.Printf("Read chat messages failed: %v", err)
		http.Error(w, "Could not read messages", http.StatusInternalServerError)
		return
	}

	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(messages)
}
