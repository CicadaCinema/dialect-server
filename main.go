package main

import (
	postHandler "dialect-server/api/post"
	voteHandler "dialect-server/api/vote"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/api/post", postHandler.Handler)
	http.HandleFunc("/api/vote", voteHandler.Handler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
