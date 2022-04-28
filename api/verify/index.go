package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v4"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type VerifyResponse struct {
	CaptchaRequired bool `json:"captchaRequired"`
}

func Handler(w http.ResponseWriter, r *http.Request) {
	// ensure we are receiving a verify request
	if r.Method == "OPTIONS" {
		return
	} else if r.Method != "GET" {
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
	var verified bool
	var captchaRequired bool
	err = conn.QueryRow(context.Background(), "SELECT restricted, restrictedmessage, verified, captchaRequired FROM Users where ip=$1;", ipAddress).Scan(&restricted, &restrictedMessage, &verified, &captchaRequired)
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
	} else if restricted {
		// check if user has been selectively restricted
		http.Error(w, restrictedMessage, http.StatusForbidden)
		return
	} else if verified {
		// if user has already passed verification, return their
		verifyResponse := VerifyResponse{
			CaptchaRequired: captchaRequired,
		}
		json.NewEncoder(w).Encode(verifyResponse)
		return
	}

	// SQL WRITE: update Users table
	if newUser {
		// new users always require a captcha
		captchaRequired = true
		_, err = conn.Exec(context.Background(), "INSERT INTO Users (ip, lastPosted, restricted, restrictedMessage, lastPostSeen, likesReceived, likesSent, dislikesReceived, dislikesSent, verified, captchaRequired) VALUES ($1, 0, false, '', 0, 0, 0, 0, 0, true, true);", ipAddress)
		if err != nil {
			// could not write new user details for some reason
			http.Error(w, "Unable to write new user's details: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// 10% chance of requiring a captcha
		captchaRequired = rand.Float64() < 0.1
		_, err = conn.Exec(context.Background(), "UPDATE Users SET verified = true, captchaRequired = $1 WHERE Ip = $2;", captchaRequired, ipAddress)
		if err != nil {
			// could not update user details for some reason
			http.Error(w, "Unable to update existing user's details: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// HTTP: finally encode and send response
	verifyResponse := VerifyResponse{
		CaptchaRequired: captchaRequired,
	}
	json.NewEncoder(w).Encode(verifyResponse)
}
