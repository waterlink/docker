package main

import (
	"log"
	"flag"
	"github.com/dotcloud/docker/server"
)

func main() {
	flag.Parse()
	d, err := server.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := d.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
