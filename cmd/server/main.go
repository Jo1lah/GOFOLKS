
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"

	"gofolks/internal/database"
	"gofolks/internal/handlers"
	"gofolks/internal/middleware"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println(".env not found; using system environment variables")
	}

	db, err := database.ConnectPostgres()
	if err != nil {
		log.Fatalf("PostgreSQL connection failed: %v", err)
	}
	defer db.Close()

	log.Println("PostgreSQL connected successfully")

	mongoClient, err := database.ConnectMongo()
	if err != nil {
		log.Fatalf("MongoDB connection failed: %v", err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := mongoClient.Disconnect(ctx); err != nil {
			log.Printf("MongoDB disconnect error: %v", err)
		}
	}()

	log.Println("MongoDB connected successfully")

	databaseName := os.Getenv("MONGO_DATABASE")
	if databaseName == "" {
		databaseName = "gofolks"
	}

	mongoDB := mongoClient.Database(databaseName)
	router := mux.NewRouter()

	router.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
			"app":    "GOFOLKS",
		})
	}).Methods(http.MethodGet)

	authHandler := &handlers.AuthHandler{DB: db}

	router.HandleFunc(
		"/api/auth/register",
		authHandler.Register,
	).Methods(http.MethodPost)

	router.HandleFunc(
		"/api/auth/login",
		authHandler.Login,
	).Methods(http.MethodPost)

	router.Handle(
		"/api/profile",
		middleware.Auth(http.HandlerFunc(authHandler.Profile)),
	).Methods(http.MethodGet)

	postHandler := &handlers.PostHandler{DB: db}

	router.HandleFunc(
		"/api/posts",
		postHandler.List,
	).Methods(http.MethodGet)

	router.HandleFunc(
		"/api/posts/{id}",
		postHandler.Get,
	).Methods(http.MethodGet)

	router.Handle(
		"/api/posts",
		middleware.Auth(http.HandlerFunc(postHandler.Create)),
	).Methods(http.MethodPost)

	router.Handle(
		"/api/posts/{id}",
		middleware.Auth(http.HandlerFunc(postHandler.Update)),
	).Methods(http.MethodPut)

	router.Handle(
		"/api/posts/{id}",
		middleware.Auth(http.HandlerFunc(postHandler.Delete)),
	).Methods(http.MethodDelete)

	activityHandler := &handlers.ActivityHandler{DB: db}

	router.HandleFunc(
		"/api/activities",
		activityHandler.List,
	).Methods(http.MethodGet)

	router.Handle(
		"/api/activities",
		middleware.Auth(http.HandlerFunc(activityHandler.Create)),
	).Methods(http.MethodPost)

	router.Handle(
		"/api/activities/mine",
		middleware.Auth(http.HandlerFunc(activityHandler.MyActivities)),
	).Methods(http.MethodGet)

	router.Handle(
		"/api/activities/{id}/join",
		middleware.Auth(http.HandlerFunc(activityHandler.Join)),
	).Methods(http.MethodPost)

	router.Handle(
		"/api/activities/{id}/leave",
		middleware.Auth(http.HandlerFunc(activityHandler.Leave)),
	).Methods(http.MethodPost)

	chatHandler := &handlers.ChatHandler{
		Collection: mongoDB.Collection("messages"),
		DB:         db,
	}

	router.HandleFunc(
		"/api/chat/ws",
		chatHandler.WebSocket,
	).Methods(http.MethodGet)

	router.Handle(
		"/api/chat/history",
		middleware.Auth(http.HandlerFunc(chatHandler.History)),
	).Methods(http.MethodGet)

	router.Handle(
		"/api/chat/ticket",
		middleware.Auth(http.HandlerFunc(middleware.IssueWebSocketTicket)),
	).Methods(http.MethodPost)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("GOFOLKS server started at http://localhost:%s", port)

	if err := server.ListenAndServe(); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server failed: %v", err)
	}
}
