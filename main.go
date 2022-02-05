package main

import (
	postHandler "dialect-server/post"
	voteHandler "dialect-server/vote"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/post", postHandler.Handler)
	http.HandleFunc("/vote", voteHandler.Handler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
