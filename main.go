package main

import (
	"fmt"
	"log"
	"os"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println(buildInfoText())
			return
		case "--extract-defaults":
			report, err := installEmbeddedDefaults(".")
			if err != nil {
				log.Fatal("extract defaults error:", err)
			}
			fmt.Println(report.Summary())
			return
		}
	}

	app, err := NewApp()
	if err != nil {
		log.Fatal("app init error:", err)
	}
	defer app.Close()

	server := app.Server()
	log.Println("Server started on " + server.Addr + " (" + buildInfoText() + ")")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
