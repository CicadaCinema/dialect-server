package main

import (
	postHandler "dialect-server/api/post"
	verifyHandler "dialect-server/api/verify"
	voteHandler "dialect-server/api/vote"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/api/post", postHandler.Handler)
	http.HandleFunc("/api/vote", voteHandler.Handler)
	http.HandleFunc("/api/verify", verifyHandler.Handler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
