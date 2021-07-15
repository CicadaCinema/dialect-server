package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v4"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var wordBlacklist = strings.Fields(os.Getenv("BLACKLIST"))

func Handler(w http.ResponseWriter, r *http.Request) {
	var err error
	startTime := time.Now()

	// set essential headers
	w.Header().Set("Content-type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Expose-Headers", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

	// ensure we are receiving a post request
	fmt.Println("DEBUG: incoming", r.Method)
	if r.Method == "OPTIONS" {
		fmt.Fprintf(w, "all good :)")
		return
	} else if r.Method != "POST" {
		http.Error(w, "Method is invalid", http.StatusMethodNotAllowed)
		return
	}

	// get misc info about this request
	ipAddress := r.Header.Get("x-real-ip")
	captchaToken := r.Header.Get("captcha-token")
	if ipAddress == "" {
		http.Error(w, "Ip address not received", http.StatusBadRequest)
		return
	} else if captchaToken == "" {
		http.Error(w, "Captcha token empty", http.StatusBadRequest)
		return
	}

	// process request body
	defer r.Body.Close()
	var newPostString string
	if requestBody, err := io.ReadAll(r.Body); err != nil {
		http.Error(w, "Unable to read request body: "+err.Error(), http.StatusInternalServerError)
		return
	} else if len(requestBody) == 0 {
		// body is empty
		http.Error(w, "Empty post", http.StatusBadRequest)
		return
	} else {
		newPostString = string(requestBody)
		for i := 0; i < len(wordBlacklist); i++ {
			if strings.Contains(strings.ToLower(newPostString), wordBlacklist[i]) {
				// body has a blacklisted word
				http.Error(w, "Post rejected", http.StatusBadRequest)
				return
			}
		}
	}

	// set up data structure for decoding request
	type GoogleResponse struct {
		Success bool
	}
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

	// connect to database
	conn, err := pgx.Connect(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		http.Error(w, "Unable to connect to database: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close(context.Background())

	// attempt to read user profile
	newUser := false
	var restricted bool
	var restrictedMessage string
	var lastPosted int64
	err = conn.QueryRow(context.Background(), "SELECT restricted, restrictedmessage, lastposted FROM Users where ip=$1;", ipAddress).Scan(&restricted, &restrictedMessage, &lastPosted)
	if err == pgx.ErrNoRows {
		// this is user's first time posting here
		newUser = true
		// so check ip address first
		resp, err := http.Get(fmt.Sprintf("http://check.getipintel.net/check.php?ip=%s&contact=email-1@example.com", ipAddress))
		if err != nil {
			http.Error(w, "Unable to verify IP address with getipintel: "+err.Error(), http.StatusInternalServerError)
			return
		} else if body, err := io.ReadAll(resp.Body); err != nil {
			http.Error(w, "Unable to read getipintel's response body: "+err.Error(), http.StatusInternalServerError)
			return
		} else {
			fmt.Println("DEBUG: getipintel's result for", ipAddress, "is", string(body))
			floatResult, err := strconv.ParseFloat(string(body), 64)
			if err != nil {
				http.Error(w, "Unable to parse getipintel's result as float64: "+err.Error(), http.StatusInternalServerError)
				return
			} else if floatResult > 0.90 {
				http.Error(w, "Usage through a VPN or proxy is not permitted.", http.StatusForbidden)
				return
			}
		}
		defer resp.Body.Close()
	} else if err != nil {
		http.Error(w, "Unable to read user profile: "+err.Error(), http.StatusInternalServerError)
		return
	} else {
		// the user profile exists
		// so check if blocked first
		// then check for cool-down
		if restricted {
			http.Error(w, restrictedMessage, http.StatusForbidden)
			return
		} else if startTime.Unix() < lastPosted+30 {
			http.Error(w, "Please wait 30 seconds before posting again.", http.StatusForbidden)
			return
		}
	}

	// everything looks good
	// let's find a post to show
	var postContent string
	var postId int
	err = conn.QueryRow(context.Background(), "SELECT content, id FROM Posts WHERE ip != $1 AND Hidden = false ORDER BY RANDOM() LIMIT 1;", ipAddress).Scan(&postContent, &postId)
	if err == pgx.ErrNoRows {
		// could not find any posts to show for some reason
		http.Error(w, "No posts found.", http.StatusInternalServerError)
		return
	} else if err != nil {
		// could not find post to show for some reason
		http.Error(w, "Unable to retrieve post: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("post-id", strconv.Itoa(postId))

	// update Users table
	if newUser {
		_, err = conn.Exec(context.Background(), "INSERT INTO Users (ip, lastPosted, restricted, restrictedMessage, lastPostSeen, likesReceived, likesSent, dislikesReceived, dislikesSent) VALUES ($1, $2, false, '', $3, 0, 0, 0, 0);", ipAddress, strconv.FormatInt(startTime.Unix(), 10), postId)
		if err != nil {
			// could not write post for some reason
			http.Error(w, "Unable to update new user's details: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		_, err = conn.Exec(context.Background(), "UPDATE Users SET LastPosted = $1, LastPostSeen = $2 WHERE Ip = $3;", strconv.FormatInt(startTime.Unix(), 10), postId, ipAddress)
		if err != nil {
			// could not write post for some reason
			http.Error(w, "Unable to update existing user's details: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// update Posts table
	_, err = conn.Exec(context.Background(), "INSERT INTO Posts (Timestamp, Content, Ip, Hidden, Likes, Dislikes) VALUES ($1, $2, $3, false, 0, 0);", strconv.FormatInt(startTime.Unix(), 10), newPostString, ipAddress)
	if err != nil {
		// could not write post for some reason
		http.Error(w, "Unable to write new post: "+err.Error(), http.StatusInternalServerError)
		return
	}

	//fmt.Fprintf(w, "<h1>Hello from Go! Version: %s</h1>", runtime.Version())

	// finally show chosen post
	fmt.Fprintf(w, postContent)
	fmt.Println("DEBUG: this successful request took", time.Since(startTime).Milliseconds(), "ms")
}
