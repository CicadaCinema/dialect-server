package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v4"
	"net/http"
	"os"
	"strings"
)

type IncomingVoteRequest struct {
	PostId     int  `json:"postId"`
	VoteAction bool `json:"voteAction"`
}

type VoteItem struct {
	Likes    int `json:"likes"`
	Dislikes int `json:"dislikes"`
	Id       int `json:"id"`
}

func Handler(w http.ResponseWriter, r *http.Request) {
	var err error

	// set headers necessary for local development
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Captcha-Token")

	// ensure we are receiving a vote request
	if r.Method == "OPTIONS" {
		return
	} else if r.Method != "POST" {
		http.Error(w, "Method is invalid", http.StatusMethodNotAllowed)
		return
	}

	// get misc info about this request
	ipAddress := "1.2.3.4"
	if !strings.HasPrefix(r.RemoteAddr, "[::1]") {
		ipAddress = r.RemoteAddr
	}
	if ipAddress == "" {
		http.Error(w, "Ip address not received", http.StatusBadRequest)
		return
	}

	// process request body
	var incomingRequest IncomingVoteRequest
	defer r.Body.Close()
	err = json.NewDecoder(r.Body).Decode(&incomingRequest)
	if err != nil {
		http.Error(w, "Unable to decode json request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// SQL: connect to database
	conn, err := pgx.Connect(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		http.Error(w, "Unable to connect to database: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close(context.Background())

	// SQL READ: get last op seen by user and the op of the post to be voted on
	var lastPostSeen int64
	var opPost int64
	err = conn.QueryRow(context.Background(), "SELECT lastpostseen FROM Users where ip=$1;", ipAddress).Scan(&lastPostSeen)
	if err == pgx.ErrNoRows {
		// user must post before they can vote
		http.Error(w, "User not found", http.StatusBadRequest)
		return
	} else if err != nil {
		http.Error(w, "Unable to read vote sender user profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	err = conn.QueryRow(context.Background(), "SELECT op FROM Posts where id=$1;", incomingRequest.PostId).Scan(&opPost)
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

	// SQL READ: get recipient's ip
	var recipientIp string
	err = conn.QueryRow(context.Background(), "SELECT ip FROM Posts WHERE id = $1;", incomingRequest.PostId).Scan(&recipientIp)
	if err == pgx.ErrNoRows {
		// silently fail if post's original author does not exist
		// TODO: find a cleaner way to do this
		return
	} else if err != nil {
		http.Error(w, "Unable to read vote recipient user profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// SQL WRITE: perform sql operation on both user profiles and the post which is being voted on
	var voteNoun string
	if incomingRequest.VoteAction {
		voteNoun = "Likes"
	} else {
		voteNoun = "Dislikes"
	}
	// batch copied from below example
	// https://github.com/jackc/pgx/blob/38cd1b40aab7244bd3d593d5153619e03b09edca/batch_test.go#L30
	batch := &pgx.Batch{}
	batch.Queue(fmt.Sprintf("UPDATE Users SET LastPostSeen = 0, %[1]sSent = %[1]sSent + 1 WHERE ip = $1;", voteNoun), ipAddress)
	batch.Queue(fmt.Sprintf("UPDATE Users SET %[1]sReceived = %[1]sReceived + 1 WHERE Ip = $1;", voteNoun), recipientIp)
	batch.Queue(fmt.Sprintf("UPDATE Posts SET %[1]s = %[1]s + 1 WHERE Id = $1;", voteNoun), incomingRequest.PostId)
	br := conn.SendBatch(context.Background(), batch)
	_, err = br.Exec()
	if err != nil {
		http.Error(w, "Unable to perform vote operation on database: "+err.Error(), http.StatusInternalServerError)
		return
	}
	err = br.Close()
	if err != nil {
		http.Error(w, "Unable to close BatchResults: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// SQL READ: find the thread which is being voted on
	rows, err := conn.Query(context.Background(), "SELECT likes, dislikes, id FROM Posts WHERE op = $1 ORDER BY Path;", opPost)
	if err == pgx.ErrNoRows {
		// could not find any threads at all
		http.Error(w, "No threads found.", http.StatusInternalServerError)
		return
	} else if err != nil {
		// could not find thread to show for some other reason
		http.Error(w, "Unable to retrieve thread: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	// SQL READ: populate slices with row contents to get the votes for each reply
	retrievedVotes := make([]VoteItem, 0)
	for rows.Next() {
		var postLikes int
		var postDislikes int
		var postId int
		err := rows.Scan(&postLikes, &postDislikes, &postId)
		if err != nil {
			http.Error(w, "Unable to scan post in thread: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// add scanned values to slices
		retrievedVotes = append(retrievedVotes, VoteItem{
			Likes:    postLikes,
			Dislikes: postDislikes,
			Id:       postId,
		})
	}

	// HTTP: finally show retrieved votes
	json.NewEncoder(w).Encode(retrievedVotes)
}
