package main

import (
	"flag"
	"log"
	"os"
	"path"
	"github.com/dotcloud/docker/client"
	"github.com/dotcloud/docker/server"
)

func main() {
	if cmd := path.Base(os.Args[0]); cmd == "docker" {
		fl_shell := flag.Bool("i", false, "Interactive client mode")
		fl_daemon := flag.Bool("d", false, "Daemon mode")
		flag.Parse()
		if *fl_shell && *fl_daemon {
			flag.Usage()
			return
		}
		// `docker -d` : daemon mode
		if *fl_daemon {
			d, err := server.New()
			if err != nil {
				log.Fatal(err)
			}
			if err := d.ListenAndServe(); err != nil {
				log.Fatal(err)
			}
		// `docker -i`: interactive client mode
		} else if *fl_shell {
			if err := client.InteractiveMode(flag.Args()...); err != nil {
				log.Fatal(err)
			}
		// `docker [COMMAND]`: simple client mode
		} else {
			if err := client.SimpleMode(os.Args[1:]); err != nil {
				log.Fatal(err)
			}
		}
	// `other_command`: "busybox mode" (symlinked as another command)
	} else {
		if err := client.SimpleMode(append([]string{cmd}, os.Args[1:]...)); err != nil {
			log.Fatal(err)
		}
	}
}
