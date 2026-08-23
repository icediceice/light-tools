package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: candidateserve <directory>")
	}
	root, err := filepath.Abs(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		log.Fatal(err)
	}
	if !info.IsDir() {
		log.Fatalf("%s is not a directory", root)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("http://%s\n", listener.Addr())
	_ = os.Stdout.Sync()

	server := &http.Server{Handler: http.FileServer(http.Dir(root))}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
