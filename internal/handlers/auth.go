package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"gofolks/internal/middleware"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	DB *pgxpool.Pool
}

type authRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type publicUser struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type authResponse struct {
	Token string     `json:"token"`
	User  publicUser `json:"user"`
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var input authRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON body",
		})
		return
	}

	input.Username = strings.TrimSpace(input.Username)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))

	if len(input.Username) < 3 || len(input.Username) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Username must be 3-50 characters",
		})
		return
	}

	if len(input.Password) < 8 || len(input.Password) > 72 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Password must be 8-72 bytes",
		})
		return
	}

	if !regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`).MatchString(input.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid email address",
		})
		return
	}

	hash, err := bcrypt.GenerateFromPassword(
		[]byte(input.Password),
		bcrypt.DefaultCost,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not secure password",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var user publicUser
	err = h.DB.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id, username, email, created_at
	`, input.Username, input.Email, string(hash)).Scan(
		&user.ID, &user.Username, &user.Email, &user.CreatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "Username or email already exists",
			})
			return
		}

		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not create user",
		})
		return
	}

	token, err := createToken(user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not create access token",
		})
		return
	}

	writeJSON(w, http.StatusCreated, authResponse{
		Token: token,
		User:  user,
	})
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var input authRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid JSON body",
		})
		return
	}

	input.Email = strings.ToLower(strings.TrimSpace(input.Email))

	if input.Email == "" || input.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Email and password are required",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var user publicUser
	var passwordHash string

	err := h.DB.QueryRow(ctx, `
		SELECT id, username, email, password_hash, created_at
		FROM users
		WHERE email = $1
	`, input.Email).Scan(
		&user.ID, &user.Username, &user.Email,
		&passwordHash, &user.CreatedAt,
	)

	if errors.Is(err, pgx.ErrNoRows) ||
		(err == nil && bcrypt.CompareHashAndPassword(
			[]byte(passwordHash), []byte(input.Password),
		) != nil) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Invalid email or password",
		})
		return
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not authenticate user",
		})
		return
	}

	token, err := createToken(user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not create access token",
		})
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Token: token,
		User:  user,
	})
}

func (h *AuthHandler) Profile(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int64)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Unauthorized",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var user publicUser
	err := h.DB.QueryRow(ctx, `
		SELECT id, username, email, created_at
		FROM users
		WHERE id = $1
	`, userID).Scan(
		&user.ID, &user.Username, &user.Email, &user.CreatedAt,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "User not found",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Could not load profile",
		})
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func createToken(userID int64) (string, error) {
	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 {
		return "", errors.New("JWT_SECRET must contain at least 32 characters")
	}

	now := time.Now()
	claims := middleware.Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func decodeJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)

	if err := decoder.Decode(dst); err != nil {
		return err
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain exactly one JSON value")
	}

	return nil
}
