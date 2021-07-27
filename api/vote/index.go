package handler

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v4"
	"io"
	"net/http"
	"os"
	"strconv"
)

func Handler(w http.ResponseWriter, r *http.Request) {
	var err error

	// set essential headers
	w.Header().Set("Content-type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Expose-Headers", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

	// ensure we are receiving a vote request
	if r.Method == "OPTIONS" {
		fmt.Fprintf(w, "all good :)")
		return
	} else if r.Method != "POST" {
		http.Error(w, "Method is invalid", http.StatusMethodNotAllowed)
		return
	}

	// get misc info about this request
	ipAddress := r.Header.Get("x-real-ip")
	if ipAddress == "" {
		http.Error(w, "Ip address not received", http.StatusBadRequest)
		return
	}

	// process request body
	defer r.Body.Close()
	var postId int64
	if requestBody, err := io.ReadAll(r.Body); err != nil {
		http.Error(w, "Unable to read request body: "+err.Error(), http.StatusInternalServerError)
		return
	} else if len(requestBody) == 0 {
		// body is empty
		http.Error(w, "Post id not found", http.StatusBadRequest)
		return
	} else {
		postId, err = strconv.ParseInt(string(requestBody), 10, 64)
		if err != nil {
			http.Error(w, "Post id malformed", http.StatusBadRequest)
			return
		}
	}

	// connect to database
	conn, err := pgx.Connect(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		http.Error(w, "Unable to connect to database: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close(context.Background())

	// get last op seen by user and the op of the post to be voted on
	var lastPostSeen int64
	var opPost int64
	err = conn.QueryRow(context.Background(), "SELECT lastpostseen FROM Users where ip=$1;", ipAddress).Scan(&lastPostSeen)
	if err == pgx.ErrNoRows {
		http.Error(w, "User not found", http.StatusBadRequest)
		return
	} else if err != nil {
		http.Error(w, "Unable to read vote sender user profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	err = conn.QueryRow(context.Background(), "SELECT op FROM Posts where id=$1;", postId).Scan(&opPost)
	if err == pgx.ErrNoRows {
		http.Error(w, "Post not found", http.StatusBadRequest)
		return
	} else if err != nil {
		http.Error(w, "Unable to read post to be voted: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if opPost != lastPostSeen {
		http.Error(w, "User cannot vote on this post", http.StatusForbidden)
		return
	}

	// get recipient's ip
	var recipientIp string
	err = conn.QueryRow(context.Background(), "SELECT ip FROM Posts WHERE id = $1;", postId).Scan(&recipientIp)
	if err == pgx.ErrNoRows {
		// silently fail if post's original author does not exist
		// TODO: find a cleaner way to do this
		return
	} else if err != nil {
		http.Error(w, "Unable to read vote recipient user profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// actually perform sql operation on the post itself and both user profiles
	switch voteAction := r.Header.Get("vote-action"); voteAction {
	case "like":
		_, err = conn.Exec(context.Background(), "UPDATE Users SET LastPostSeen = 0, LikesSent = LikesSent + 1 WHERE ip = $1;", ipAddress)
		if err != nil {
			http.Error(w, "Unable to perform vote operation on database (01): "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = conn.Exec(context.Background(), "UPDATE Users SET LikesReceived = LikesReceived + 1 WHERE Ip = $1;", recipientIp)
		if err != nil {
			http.Error(w, "Unable to perform vote operation on database (02): "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = conn.Exec(context.Background(), "UPDATE Posts SET Likes = Likes + 1 WHERE Id = $1;", postId)
		if err != nil {
			http.Error(w, "Unable to perform vote operation on database (03): "+err.Error(), http.StatusInternalServerError)
			return
		}
	case "dislike":
		_, err = conn.Exec(context.Background(), "UPDATE Users SET LastPostSeen = 0, DislikesSent = DislikesSent + 1 WHERE ip = $1;", ipAddress)
		if err != nil {
			http.Error(w, "Unable to perform vote operation on database (04): "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = conn.Exec(context.Background(), "UPDATE Users SET DislikesReceived = DislikesReceived + 1 WHERE Ip = $1;", recipientIp)
		if err != nil {
			http.Error(w, "Unable to perform vote operation on database (05): "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = conn.Exec(context.Background(), "UPDATE Posts SET Dislikes = Dislikes + 1 WHERE Id = $1;", postId)
		if err != nil {
			http.Error(w, "Unable to perform vote operation on database (06): "+err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "Vote action malformed or non existent", http.StatusBadRequest)
		return
	}

	fmt.Fprintf(w, "Your request has been processed.")
}
