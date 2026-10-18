
package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type wsTicket struct {
	UserID    int64
	ExpiresAt time.Time
}

var wsTickets = struct {
	sync.Mutex
	items map[string]wsTicket
}{
	items: make(map[string]wsTicket),
}

// IssueWebSocketTicket выдаёт одноразовый билет авторизованному пользователю.
func IssueWebSocketTicket(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(UserIDKey).(int64)
	if !ok || userID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		http.Error(w, "Could not create ticket", http.StatusInternalServerError)
		return
	}

	ticket := base64.RawURLEncoding.EncodeToString(randomBytes)

	wsTickets.Lock()
	now := time.Now()

	for key, value := range wsTickets.items {
		if now.After(value.ExpiresAt) {
			delete(wsTickets.items, key)
		}
	}

	wsTickets.items[ticket] = wsTicket{
		UserID:    userID,
		ExpiresAt: now.Add(30 * time.Second),
	}
	wsTickets.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"ticket": ticket,
	})
}

// ConsumeWebSocketTicket проверяет билет и сразу аннулирует его.
func ConsumeWebSocketTicket(ticket string) (int64, bool) {
	if ticket == "" {
		return 0, false
	}

	wsTickets.Lock()
	defer wsTickets.Unlock()

	value, exists := wsTickets.items[ticket]
	delete(wsTickets.items, ticket)

	if !exists || time.Now().After(value.ExpiresAt) {
		return 0, false
	}

	return value.UserID, true
}
