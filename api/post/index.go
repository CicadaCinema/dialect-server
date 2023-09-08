package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v4"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var wordBlacklist = strings.Fields(os.Getenv("BLACKLIST"))

type IncomingPostRequest struct {
	PostContent string `json:"postContent"`
	ReplyId     int    `json:"replyId"`
}

type PostItem struct {
	PostContent string `json:"postContent"`
	Path        string `json:"path"`
	Id          int    `json:"id"`
}

type GoogleResponse struct {
	Success bool
}

func Handler(w http.ResponseWriter, r *http.Request) {
	var err error
	startTime := time.Now()

	// set headers necessary for local development
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Captcha-Token")

	// ensure we are receiving a post request
	fmt.Println("DEBUG: incoming", r.Method)
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
		// but the ipAddress is set to 1.2.3.4 before we even look at the request?
		// need to make sure that ip is only 1.2.3.4 when debugging/running server locally
		// TODO: fix this and the other instances of it
		http.Error(w, "Ip address not received", http.StatusBadRequest)
		return
	}

	// HTTP: process request body
	var incomingRequest IncomingPostRequest
	defer r.Body.Close()
	err = json.NewDecoder(r.Body).Decode(&incomingRequest)
	if err != nil {
		http.Error(w, "Unable to decode json request body: "+err.Error(), http.StatusBadRequest)
		return
	} else if len(incomingRequest.PostContent) == 0 {
		// post is empty
		http.Error(w, "Empty post", http.StatusBadRequest)
		return
	} else {
		for i := 0; i < len(wordBlacklist); i++ {
			if strings.Contains(strings.ToLower(incomingRequest.PostContent), wordBlacklist[i]) {
				// body has a blacklisted word
				http.Error(w, "Post rejected", http.StatusBadRequest)
				return
			}
		}
	}

	// angel mode masks client's real ip address
	if len(incomingRequest.PostContent) >= 4 && incomingRequest.PostContent[:4] == "££" {
		ipAddress = "1.1.1.1"
		// unless it would result in an empty post, remove the leading "££" (four characters in reality)
		if incomingRequest.PostContent != "££" {
			incomingRequest.PostContent = incomingRequest.PostContent[4:]
		}
	}

	// SQL: connect to database
	conn, err := pgx.Connect(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		http.Error(w, "Unable to connect to database: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close(context.Background())

	// SQL READ: attempt to read user profile
	var verified bool
	var captchaRequired bool
	var lastPosted int64
	err = conn.QueryRow(context.Background(), "SELECT verified, captcharequired, lastposted FROM Users WHERE ip=$1;", ipAddress).Scan(&verified, &captchaRequired, &lastPosted)
	if err == pgx.ErrNoRows {
		// user must verify before they can post
		http.Error(w, "User not found", http.StatusBadRequest)
		return
	} else if err != nil {
		http.Error(w, "Unable to read user profile: "+err.Error(), http.StatusInternalServerError)
		return
	} else if startTime.Unix() < lastPosted+15 {
		http.Error(w, "Please wait 15 seconds before posting again.", http.StatusForbidden)
		return
	} else if !verified {
		// user must verify before they can post
		http.Error(w, "User isn't verified.", http.StatusForbidden)
		return
	}

	// HTTP: check captcha only if necessary
	if captchaRequired {
		captchaToken := r.Header.Get("captcha-token")
		if captchaToken == "" {
			http.Error(w, "Captcha token empty", http.StatusBadRequest)
			return
		}

		// set up data structure for decoding request
		var captchaResponse GoogleResponse

		// verify captcha token by sending request to Google
		resp, err := http.Get(fmt.Sprintf("https://www.google.com/recaptcha/api/siteverify?secret=%s&response=%s", os.Getenv("RECAPTCHA_SECRET"), captchaToken))
		if err != nil {
			http.Error(w, "Unable to verify captcha with Google: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// attempt to decode this request
		err = json.NewDecoder(resp.Body).Decode(&captchaResponse)
		if err != nil {
			http.Error(w, "Unable to read Google's response body: "+err.Error(), http.StatusInternalServerError)
			return
		} else {
			// read request properly
			if !captchaResponse.Success {
				http.Error(w, "Invalid captcha.", http.StatusForbidden)
				return
			}
		}
		defer resp.Body.Close()
	}

	// SQL READ/WRITE: find a thread to show, update view count
	// this updates the view count of every post in that thread using the same SQL query, use https://stackoverflow.com/a/25650188 as reference
	rows, err := conn.Query(context.Background(), "WITH updated AS (UPDATE Posts SET views = views + 1 WHERE op = (SELECT op FROM Posts WHERE ip != $1 AND Hidden = false ORDER BY RANDOM() LIMIT 1) RETURNING *) SELECT content, id, path, op FROM updated ORDER BY Path;", ipAddress)
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

	// SQL READ: populate slices with row contents to get data for each reply
	retrievedPosts := make([]PostItem, 0)
	var threadOp int
	for rows.Next() {
		var postContent string
		var postPath string
		var postId int
		// there is a small inefficiency here:
		// we scan every post/reply for its OP even though it will be the same for each one
		// (we need to update the Users table using threadOp later)
		err := rows.Scan(&postContent, &postId, &postPath, &threadOp)
		if err != nil {
			http.Error(w, "Unable to scan post in thread: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// add scanned values to slices
		retrievedPosts = append(retrievedPosts, PostItem{
			PostContent: postContent,
			Path:        postPath,
			Id:          postId,
		})
	}

	// SQL WRITE: update Users table - post viewer
	_, err = conn.Exec(context.Background(), "UPDATE Users SET LastPosted = $1, LastPostSeen = $2, verified = false, viewsSent = viewsSent + 1 WHERE Ip = $3;", strconv.FormatInt(startTime.Unix(), 10), threadOp, ipAddress)
	if err != nil {
		// could not update user details for some reason
		http.Error(w, "Unable to update post viewer user's details: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// SQL WRITE: update Users table - post author(s)
	_, err = conn.Exec(context.Background(), "UPDATE Users SET viewsReceived = viewsReceived + 1 WHERE Ip IN (SELECT Ip from Posts WHERE Op = $1);", threadOp)
	if err != nil {
		// could not update user details for some reason
		http.Error(w, "Unable to update post author(s) user's details: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// SQL WRITE: write new post to Posts table
	if incomingRequest.ReplyId == 0 {
		// new post
		_, err = conn.Exec(context.Background(), "INSERT INTO Posts (Timestamp, Content, Ip) VALUES ($1, $2, $3);", strconv.FormatInt(startTime.Unix(), 10), incomingRequest.PostContent, ipAddress)
		if err != nil {
			http.Error(w, "Unable to write new post (01): "+err.Error(), http.StatusInternalServerError)
			return
		}
		// fill in remaining fields now that we know what Id is
		// TODO: in production I saw two cases of OP being null, must add further detailed logging to try and catch this bug
		_, err = conn.Exec(context.Background(), "UPDATE Posts SET Op = Id, Path = CONCAT('/',CAST(Id AS VARCHAR)) WHERE Op IS NULL AND Path IS NULL;")
		if err != nil {
			http.Error(w, "Unable to write new post (02): "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// reply
		var tempId int
		err = conn.QueryRow(context.Background(), "INSERT INTO Posts (Timestamp, Content, Ip, Op, Path) VALUES ($1, $2, $3, (SELECT Op FROM Posts WHERE Id = $4), (SELECT Path FROM Posts WHERE Id = $4)) RETURNING Id;", strconv.FormatInt(startTime.Unix(), 10), incomingRequest.PostContent, ipAddress, incomingRequest.ReplyId).Scan(&tempId)
		if err != nil {
			http.Error(w, "Unable to write new post (03): "+err.Error(), http.StatusInternalServerError)
			return
		}
		// fill in remaining fields now that we know what Id is
		_, err = conn.Exec(context.Background(), "UPDATE Posts SET Path = CONCAT(Path, '/', CAST(Id AS VARCHAR)) WHERE Id = $1;", tempId)
		if err != nil {
			http.Error(w, "Unable to write new post (04): "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// HTTP: finally show retrieved thread
	json.NewEncoder(w).Encode(retrievedPosts)
	fmt.Println("DEBUG: this successful request took", time.Since(startTime).Milliseconds(), "ms")
}
